package dirsync

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
)

// BlockSize is the fixed delta block size. A one-block edit in a large file
// transfers roughly one block instead of the whole file.
const BlockSize = 4096

// rollingMod bounds the weak rolling checksum (an Adler-style sum). The exact
// modulus only affects hash distribution, not correctness, because every weak
// hit is confirmed by a strong sha256 comparison.
const rollingMod = 65521

// BlockSig is the receiver-supplied signature of one of its existing blocks.
// Weak is a fast rolling checksum used to index candidate matches; Strong is a
// truncated sha256 that confirms a match (the full 32 bytes would bloat the
// signature for no practical collision benefit here).
type BlockSig struct {
	Index  uint32   `json:"i"`
	Weak   uint32   `json:"w"`
	Strong [16]byte `json:"s"`
}

// Signature is the receiver's description of a file it already has: the list of
// its fixed-size blocks. The sender uses it to emit copy/data instructions.
type Signature struct {
	BlockSize int        `json:"bs"`
	Blocks    []BlockSig `json:"blocks"`
}

// BuildSignature reads r (the receiver's current version of a file) and returns
// its block signature. The final block may be shorter than BlockSize.
func BuildSignature(r io.Reader) (Signature, error) {
	br := bufio.NewReaderSize(r, BlockSize*4)
	sig := Signature{BlockSize: BlockSize}
	buf := make([]byte, BlockSize)
	var idx uint32
	for {
		n, err := io.ReadFull(br, buf)
		if n > 0 {
			block := buf[:n]
			sum := sha256.Sum256(block)
			var strong [16]byte
			copy(strong[:], sum[:16])
			sig.Blocks = append(sig.Blocks, BlockSig{
				Index:  idx,
				Weak:   weakSum(block),
				Strong: strong,
			})
			idx++
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return Signature{}, err
		}
	}
	return sig, nil
}

// weakSum computes an Adler-style rolling checksum over a byte slice. It is the
// non-incremental form; the incremental roller below must agree with it.
func weakSum(block []byte) uint32 {
	a, b := initRoll(block)
	return (b << 16) | a
}

// initRoll computes the (a, b) state of the rolling checksum over a full block.
// a is the byte sum mod M; b is the sum of running prefix sums mod M.
func initRoll(block []byte) (a, b uint32) {
	for _, c := range block {
		a = (a + uint32(c)) % rollingMod
		b = (b + a) % rollingMod
	}
	return a, b
}

// rollByte advances the rolling checksum by removing the byte that leaves the
// front of the window (out) and adding the byte that enters the back (in),
// where blockLen is the window length. It is the O(1) counterpart of initRoll
// and must produce identical (a, b) to recomputing initRoll over the new window.
func rollByte(a, b, out, in, blockLen uint32) (uint32, uint32) {
	// a' = a - out + in
	a = (a + rollingMod - (out % rollingMod)) % rollingMod
	a = (a + in) % rollingMod
	// b' = b - blockLen*out + a'
	sub := (blockLen % rollingMod) * (out % rollingMod) % rollingMod
	b = (b + rollingMod - sub) % rollingMod
	b = (b + a) % rollingMod
	return a, b
}

// Op codes for delta instructions on the wire.
const (
	opData = byte(1) // followed by uint32 length + that many literal bytes
	opCopy = byte(2) // followed by uint32 receiver block index to copy
	opEnd  = byte(3) // end of instruction stream
)

// strongFull computes the 16-byte truncated sha256 of a block.
func strongFull(block []byte) [16]byte {
	sum := sha256.Sum256(block)
	var s [16]byte
	copy(s[:], sum[:16])
	return s
}

// WriteDelta reads the sender's current file from src and, using the receiver's
// Signature, writes a stream of opData/opCopy instructions to dst that lets the
// receiver reconstruct src. It returns the number of literal (non-copied) bytes
// emitted, which is the useful "bytes transferred" measure for a file.
//
// The matcher slides a window byte by byte, maintaining the rolling checksum so
// an insertion or deletion realigns to the next matching block rather than
// invalidating the whole file.
func WriteDelta(dst io.Writer, src io.Reader, sig Signature) (int64, error) {
	bs := sig.BlockSize
	if bs <= 0 {
		bs = BlockSize
	}

	// Index receiver blocks by weak checksum -> list of (index, strong).
	type cand struct {
		index  uint32
		strong [16]byte
	}
	index := make(map[uint32][]cand, len(sig.Blocks))
	for _, b := range sig.Blocks {
		index[b.Weak] = append(index[b.Weak], cand{b.Index, b.Strong})
	}

	data, err := io.ReadAll(src)
	if err != nil {
		return 0, err
	}

	var literal int64
	// pending marks the start of literal bytes not yet flushed.
	pending := 0

	flush := func(end int) error {
		if end <= pending {
			return nil
		}
		chunk := data[pending:end]
		literal += int64(len(chunk))
		if err := writeOpData(dst, chunk); err != nil {
			return err
		}
		pending = end
		return nil
	}

	i := 0
	n := len(data)
	// Maintain the rolling checksum (a, b) incrementally. When the window does
	// not match, slide it one byte forward in O(1) instead of recomputing the
	// full weakSum, keeping large files cheap.
	var a, b uint32
	haveRoll := false
	for i < n {
		// Remaining bytes shorter than a full block can never match a full
		// block; emit them as literal and stop.
		end := i + bs
		if end > n {
			break
		}
		if !haveRoll {
			a, b = initRoll(data[i:end])
			haveRoll = true
		}
		w := (b << 16) | a
		matched := false
		if cands, ok := index[w]; ok {
			s := strongFull(data[i:end])
			for _, c := range cands {
				if c.strong == s {
					// Flush literals before the match, then copy.
					if err := flush(i); err != nil {
						return 0, err
					}
					if err := writeOpCopy(dst, c.index); err != nil {
						return 0, err
					}
					i = end
					pending = i
					haveRoll = false // window jumps; recompute next time
					matched = true
					break
				}
			}
		}
		if !matched {
			// Slide one byte: remove data[i], add data[i+bs] (if present).
			if i+bs < n {
				a, b = rollByte(a, b, uint32(data[i]), uint32(data[i+bs]), uint32(bs))
			} else {
				haveRoll = false
			}
			i++
		}
	}
	// Flush any trailing literals (the unmatched tail and short final bytes).
	if err := flush(n); err != nil {
		return 0, err
	}
	if err := writeByte(dst, opEnd); err != nil {
		return 0, err
	}
	return literal, nil
}

// ApplyDelta reconstructs a file from the sender's instruction stream. base is
// the receiver's current version (used to satisfy opCopy by block index); out
// receives the rebuilt content. It returns once an opEnd op is read.
func ApplyDelta(out io.Writer, base io.ReaderAt, baseSize int64, blockSize int, r io.Reader) error {
	bs := blockSize
	if bs <= 0 {
		bs = BlockSize
	}
	br := bufio.NewReader(r)
	for {
		op, err := br.ReadByte()
		if err != nil {
			return err
		}
		switch op {
		case opEnd:
			return nil
		case opData:
			length, err := readUint32(br)
			if err != nil {
				return err
			}
			if length > MaxBlockData {
				return fmt.Errorf("dirsync: opData length %d too large", length)
			}
			if _, err := io.CopyN(out, br, int64(length)); err != nil {
				return err
			}
		case opCopy:
			idx, err := readUint32(br)
			if err != nil {
				return err
			}
			off := int64(idx) * int64(bs)
			if off >= baseSize {
				return fmt.Errorf("dirsync: opCopy index %d out of range", idx)
			}
			size := int64(bs)
			if off+size > baseSize {
				size = baseSize - off
			}
			buf := make([]byte, size)
			if _, err := base.ReadAt(buf, off); err != nil && err != io.EOF {
				return err
			}
			if _, err := out.Write(buf); err != nil {
				return err
			}
		default:
			return fmt.Errorf("dirsync: unknown delta op %d", op)
		}
	}
}

// MaxBlockData caps a single opData length to avoid unbounded allocation when
// streaming. Senders never exceed it because flushes happen frequently, but a
// hostile peer must not be able to demand arbitrary memory.
const MaxBlockData = 64 << 20

func writeByte(w io.Writer, b byte) error {
	_, err := w.Write([]byte{b})
	return err
}

func writeOpData(w io.Writer, chunk []byte) error {
	// Split very large literal runs so each opData stays under MaxBlockData.
	for len(chunk) > 0 {
		n := len(chunk)
		if n > MaxBlockData {
			n = MaxBlockData
		}
		var hdr [5]byte
		hdr[0] = opData
		binary.BigEndian.PutUint32(hdr[1:], uint32(n))
		if _, err := w.Write(hdr[:]); err != nil {
			return err
		}
		if _, err := w.Write(chunk[:n]); err != nil {
			return err
		}
		chunk = chunk[n:]
	}
	return nil
}

func writeOpCopy(w io.Writer, index uint32) error {
	var b [5]byte
	b[0] = opCopy
	binary.BigEndian.PutUint32(b[1:], index)
	_, err := w.Write(b[:])
	return err
}

func readUint32(r io.Reader) (uint32, error) {
	var b [4]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(b[:]), nil
}
