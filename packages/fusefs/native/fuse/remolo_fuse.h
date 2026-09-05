#ifndef REMOLO_FUSE_H
#define REMOLO_FUSE_H

#include <stdint.h>
#include "foundation/runtime.h"

uint64_t remolo_fuse_open(const fdn_string *mountpoint);
int32_t remolo_fuse_next(uint64_t handle, uint32_t *operation,
                         void *path, void *second_path, uint64_t *offset,
                         uint64_t *size, uint32_t *mode, uint32_t *flags,
                         uint64_t *data);
int32_t remolo_fuse_add_directory_entry(uint64_t handle, const fdn_string *name,
                                        uint32_t mode);
int32_t remolo_fuse_reply_error(uint64_t handle, int32_t error);
int32_t remolo_fuse_reply_ok(uint64_t handle);
int32_t remolo_fuse_reply_attribute(uint64_t handle, uint32_t mode,
                                    uint64_t size, uint64_t modified);
int32_t remolo_fuse_reply_read(uint64_t handle, uint64_t data);
int32_t remolo_fuse_reply_write(uint64_t handle, uint64_t count);
int32_t remolo_fuse_close(uint64_t *handle);

#endif
