#define _POSIX_C_SOURCE 200112L

#include <foundation/runtime.h>

#include <stdatomic.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

#ifdef _WIN32
#include <windows.h>
#define FDN_RTC_LIBRARY HMODULE
#define fdn_rtc_open(name) LoadLibraryA(name)
#define fdn_rtc_symbol(library, name) GetProcAddress((library), (name))
#else
#include <dlfcn.h>
#include <time.h>
#define FDN_RTC_LIBRARY void *
#define fdn_rtc_open(name) dlopen((name), RTLD_NOW | RTLD_LOCAL)
#define fdn_rtc_symbol(library, name) dlsym((library), (name))
#endif

enum fdn_rtc_status {
    FDN_RTC_OK = 0,
    FDN_RTC_UNAVAILABLE = 1,
    FDN_RTC_INVALID = 2,
    FDN_RTC_FAILED = 3,
    FDN_RTC_CLOSED = 4,
};

typedef struct fdn_rtc_configuration {
    const char **ice_servers;
    int ice_servers_count;
    const char *proxy_server;
    const char *bind_address;
    int certificate_type;
    int transport_policy;
    bool enable_ice_tcp;
    bool enable_ice_udp_mux;
    bool disable_auto_negotiation;
    bool force_media_transport;
    uint16_t port_range_begin;
    uint16_t port_range_end;
    int mtu;
    int max_message_size;
} fdn_rtc_configuration;

typedef void (*fdn_rtc_gathering_callback)(int, int, void *);
typedef void (*fdn_rtc_data_channel_callback)(int, int, void *);
typedef void (*fdn_rtc_open_callback)(int, void *);
typedef void (*fdn_rtc_closed_callback)(int, void *);
typedef void (*fdn_rtc_message_callback)(int, const char *, int, void *);

typedef struct fdn_rtc_api {
    FDN_RTC_LIBRARY library;
    void (*set_user_pointer)(int, void *);
    int (*create_peer)(const fdn_rtc_configuration *);
    int (*close_peer)(int);
    int (*delete_peer)(int);
    int (*set_gathering_callback)(int, fdn_rtc_gathering_callback);
    int (*set_data_channel_callback)(int, fdn_rtc_data_channel_callback);
    int (*set_local_description)(int, const char *);
    int (*set_remote_description)(int, const char *, const char *);
    int (*get_local_description)(int, char *, int);
    int (*create_data_channel)(int, const char *);
    int (*delete_data_channel)(int);
    int (*set_open_callback)(int, fdn_rtc_open_callback);
    int (*set_closed_callback)(int, fdn_rtc_closed_callback);
    int (*set_message_callback)(int, fdn_rtc_message_callback);
    int (*send_message)(int, const char *, int);
    int (*close_channel)(int);
    bool (*is_open)(int);
} fdn_rtc_api;

typedef struct fdn_rtc_peer fdn_rtc_peer;

typedef struct fdn_rtc_message {
    unsigned char *data;
    size_t length;
    struct fdn_rtc_message *next;
} fdn_rtc_message;

typedef struct fdn_rtc_channel {
    int identifier;
    fdn_rtc_peer *peer;
    _Atomic size_t references;
    _Atomic int open;
    _Atomic int closed;
    atomic_flag lock;
    fdn_rtc_message *head;
    fdn_rtc_message *tail;
} fdn_rtc_channel;

struct fdn_rtc_peer {
    int identifier;
    _Atomic size_t references;
    _Atomic int closed;
    _Atomic int gathering;
    atomic_flag lock;
    fdn_rtc_channel *incoming;
};

static fdn_rtc_api fdn_rtc;
static atomic_flag fdn_rtc_load_lock = ATOMIC_FLAG_INIT;
static _Atomic int fdn_rtc_load_state;

static void fdn_rtc_pause(void) {
#ifdef _WIN32
    Sleep(1);
#else
    const struct timespec delay = { .tv_sec = 0, .tv_nsec = 1000000 };
    nanosleep(&delay, NULL);
#endif
}

static void fdn_rtc_lock(atomic_flag *lock) {
    while (atomic_flag_test_and_set_explicit(lock, memory_order_acquire)) {}
}

static void fdn_rtc_unlock(atomic_flag *lock) {
    atomic_flag_clear_explicit(lock, memory_order_release);
}

static int fdn_rtc_load_symbol(void **target, const char *name) {
    *target = (void *)fdn_rtc_symbol(fdn_rtc.library, name);
    return *target != NULL;
}

static int fdn_rtc_load(void) {
    int state = atomic_load_explicit(&fdn_rtc_load_state, memory_order_acquire);
    if (state != 0) return state > 0;
    fdn_rtc_lock(&fdn_rtc_load_lock);
    state = atomic_load_explicit(&fdn_rtc_load_state, memory_order_relaxed);
    if (state == 0) {
#ifdef _WIN32
        const char *names[] = { "datachannel.dll", "libdatachannel.dll", NULL };
#elif defined(__APPLE__)
        const char *names[] = {
            "libdatachannel.dylib",
            "/opt/homebrew/lib/libdatachannel.dylib",
            "/usr/local/lib/libdatachannel.dylib",
            NULL
        };
#else
        const char *names[] = {
            "libdatachannel.so.0.24",
            "libdatachannel.so",
            NULL
        };
#endif
        size_t index = 0;
        while (names[index] != NULL && fdn_rtc.library == NULL) {
            fdn_rtc.library = fdn_rtc_open(names[index++]);
        }
        if (fdn_rtc.library != NULL &&
            fdn_rtc_load_symbol((void **)&fdn_rtc.set_user_pointer,
                                "rtcSetUserPointer") &&
            fdn_rtc_load_symbol((void **)&fdn_rtc.create_peer,
                                "rtcCreatePeerConnection") &&
            fdn_rtc_load_symbol((void **)&fdn_rtc.close_peer,
                                "rtcClosePeerConnection") &&
            fdn_rtc_load_symbol((void **)&fdn_rtc.delete_peer,
                                "rtcDeletePeerConnection") &&
            fdn_rtc_load_symbol((void **)&fdn_rtc.set_gathering_callback,
                                "rtcSetGatheringStateChangeCallback") &&
            fdn_rtc_load_symbol((void **)&fdn_rtc.set_data_channel_callback,
                                "rtcSetDataChannelCallback") &&
            fdn_rtc_load_symbol((void **)&fdn_rtc.set_local_description,
                                "rtcSetLocalDescription") &&
            fdn_rtc_load_symbol((void **)&fdn_rtc.set_remote_description,
                                "rtcSetRemoteDescription") &&
            fdn_rtc_load_symbol((void **)&fdn_rtc.get_local_description,
                                "rtcGetLocalDescription") &&
            fdn_rtc_load_symbol((void **)&fdn_rtc.create_data_channel,
                                "rtcCreateDataChannel") &&
            fdn_rtc_load_symbol((void **)&fdn_rtc.delete_data_channel,
                                "rtcDeleteDataChannel") &&
            fdn_rtc_load_symbol((void **)&fdn_rtc.set_open_callback,
                                "rtcSetOpenCallback") &&
            fdn_rtc_load_symbol((void **)&fdn_rtc.set_closed_callback,
                                "rtcSetClosedCallback") &&
            fdn_rtc_load_symbol((void **)&fdn_rtc.set_message_callback,
                                "rtcSetMessageCallback") &&
            fdn_rtc_load_symbol((void **)&fdn_rtc.send_message,
                                "rtcSendMessage") &&
            fdn_rtc_load_symbol((void **)&fdn_rtc.close_channel,
                                "rtcClose") &&
            fdn_rtc_load_symbol((void **)&fdn_rtc.is_open, "rtcIsOpen")) {
            state = 1;
        } else {
            state = -1;
        }
        atomic_store_explicit(&fdn_rtc_load_state, state, memory_order_release);
    }
    fdn_rtc_unlock(&fdn_rtc_load_lock);
    return state > 0;
}

static int fdn_rtc_copy_string(const fdn_string *value, char **result) {
    char *copy;
    if (value == NULL || result == NULL ||
        memchr(value->data, '\0', value->length) != NULL) {
        return 0;
    }
    copy = malloc(value->length + 1);
    if (copy == NULL) return 0;
    memcpy(copy, value->data, value->length);
    copy[value->length] = '\0';
    *result = copy;
    return 1;
}

static int32_t fdn_rtc_output_string(const char *value, fdn_string *result) {
    size_t length;
    char *copy;
    if (value == NULL || result == NULL) return FDN_RTC_INVALID;
    length = strlen(value);
    if (!fdn_utf8_valid(value, length)) return FDN_RTC_FAILED;
    copy = fdn_alloc(length);
    if (copy == NULL && length != 0) return FDN_RTC_FAILED;
    if (length != 0) memcpy(copy, value, length);
    fdn_string_drop(result);
    result->data = copy;
    result->length = length;
    result->owned = 1;
    return FDN_RTC_OK;
}

static int32_t fdn_rtc_output_bytes(const unsigned char *value, size_t length,
                                    uint64_t *result) {
    if (result == NULL) return FDN_RTC_INVALID;
    return foundation_runtime_bytes_copy_from_raw(value, (uint64_t)length,
                                                  result) == 0
        ? FDN_RTC_OK
        : FDN_RTC_FAILED;
}

static void fdn_rtc_channel_release(fdn_rtc_channel *channel);

static void fdn_rtc_peer_clear_callbacks(fdn_rtc_peer *peer) {
    fdn_rtc.set_user_pointer(peer->identifier, NULL);
    fdn_rtc.set_gathering_callback(peer->identifier, NULL);
    fdn_rtc.set_data_channel_callback(peer->identifier, NULL);
}

static void fdn_rtc_channel_clear_callbacks(fdn_rtc_channel *channel) {
    fdn_rtc.set_user_pointer(channel->identifier, NULL);
    fdn_rtc.set_open_callback(channel->identifier, NULL);
    fdn_rtc.set_closed_callback(channel->identifier, NULL);
    fdn_rtc.set_message_callback(channel->identifier, NULL);
}

static void fdn_rtc_peer_release(fdn_rtc_peer *peer) {
    if (peer == NULL ||
        atomic_fetch_sub_explicit(&peer->references, 1,
                                  memory_order_acq_rel) != 1) {
        return;
    }
    atomic_store_explicit(&peer->closed, 1, memory_order_release);
    fdn_rtc_peer_clear_callbacks(peer);
    fdn_rtc.delete_peer(peer->identifier);
    free(peer);
}

static int fdn_rtc_peer_retain(fdn_rtc_peer *peer) {
    size_t references;
    if (peer == NULL || atomic_load_explicit(&peer->closed, memory_order_acquire)) {
        return 0;
    }
    references = atomic_load_explicit(&peer->references, memory_order_relaxed);
    while (references != 0) {
        if (atomic_compare_exchange_weak_explicit(
                &peer->references, &references, references + 1,
                memory_order_acq_rel, memory_order_relaxed)) {
            if (atomic_load_explicit(&peer->closed, memory_order_acquire)) {
                fdn_rtc_peer_release(peer);
                return 0;
            }
            return 1;
        }
    }
    return 0;
}

static void fdn_rtc_channel_opened(int identifier, void *argument) {
    fdn_rtc_channel *channel = argument;
    (void)identifier;
    if (channel != NULL) {
        atomic_store_explicit(&channel->open, 1, memory_order_release);
    }
}

static void fdn_rtc_channel_closed(int identifier, void *argument) {
    fdn_rtc_channel *channel = argument;
    (void)identifier;
    if (channel != NULL) {
        atomic_store_explicit(&channel->closed, 1, memory_order_release);
    }
}

static void fdn_rtc_channel_message(int identifier, const char *value, int size,
                                    void *argument) {
    fdn_rtc_channel *channel = argument;
    fdn_rtc_message *message;
    (void)identifier;
    if (channel == NULL || value == NULL || size < 0) return;
    message = calloc(1, sizeof(*message));
    if (message == NULL) return;
    if (size > 0) {
        message->data = malloc((size_t)size);
        if (message->data == NULL) {
            free(message);
            return;
        }
        memcpy(message->data, value, (size_t)size);
    }
    message->length = (size_t)size;
    fdn_rtc_lock(&channel->lock);
    if (channel->tail == NULL) {
        channel->head = message;
    } else {
        channel->tail->next = message;
    }
    channel->tail = message;
    fdn_rtc_unlock(&channel->lock);
}

static fdn_rtc_channel *fdn_rtc_channel_new(fdn_rtc_peer *peer,
                                             int identifier) {
    fdn_rtc_channel *channel;
    if (identifier < 0 || !fdn_rtc_peer_retain(peer)) return NULL;
    channel = calloc(1, sizeof(*channel));
    if (channel == NULL) {
        fdn_rtc_peer_release(peer);
        return NULL;
    }
    channel->identifier = identifier;
    channel->peer = peer;
    atomic_init(&channel->references, 1);
    atomic_init(&channel->open, fdn_rtc.is_open(identifier) ? 1 : 0);
    atomic_init(&channel->closed, 0);
    atomic_flag_clear(&channel->lock);
    fdn_rtc.set_user_pointer(identifier, channel);
    if (fdn_rtc.set_open_callback(identifier, fdn_rtc_channel_opened) < 0 ||
        fdn_rtc.set_closed_callback(identifier, fdn_rtc_channel_closed) < 0 ||
        fdn_rtc.set_message_callback(identifier, fdn_rtc_channel_message) < 0) {
        fdn_rtc_channel_clear_callbacks(channel);
        fdn_rtc.delete_data_channel(identifier);
        fdn_rtc_peer_release(peer);
        free(channel);
        return NULL;
    }
    return channel;
}

static void fdn_rtc_channel_release(fdn_rtc_channel *channel) {
    fdn_rtc_message *message;
    if (channel == NULL ||
        atomic_fetch_sub_explicit(&channel->references, 1,
                                  memory_order_acq_rel) != 1) {
        return;
    }
    atomic_store_explicit(&channel->closed, 1, memory_order_release);
    fdn_rtc_channel_clear_callbacks(channel);
    fdn_rtc.delete_data_channel(channel->identifier);
    message = channel->head;
    while (message != NULL) {
        fdn_rtc_message *next = message->next;
        free(message->data);
        free(message);
        message = next;
    }
    fdn_rtc_peer_release(channel->peer);
    free(channel);
}

static void fdn_rtc_gathering_changed(int identifier, int state,
                                      void *argument) {
    fdn_rtc_peer *peer = argument;
    (void)identifier;
    if (peer != NULL && state == 2) {
        atomic_store_explicit(&peer->gathering, 1, memory_order_release);
    }
}

static void fdn_rtc_data_channel(int identifier, int channel_identifier,
                                 void *argument) {
    fdn_rtc_peer *peer = argument;
    fdn_rtc_channel *channel;
    (void)identifier;
    if (peer == NULL) return;
    channel = fdn_rtc_channel_new(peer, channel_identifier);
    if (channel == NULL) return;
    fdn_rtc_lock(&peer->lock);
    if (peer->incoming == NULL) {
        peer->incoming = channel;
        channel = NULL;
    }
    fdn_rtc_unlock(&peer->lock);
    fdn_rtc_channel_release(channel);
}

int32_t remolo_rtc_peer_open(uint64_t *result) {
    fdn_rtc_configuration configuration = {0};
    fdn_rtc_peer *peer;
    int identifier;
    if (result == NULL) return FDN_RTC_INVALID;
    *result = 0;
    if (!fdn_rtc_load()) return FDN_RTC_UNAVAILABLE;
    configuration.disable_auto_negotiation = true;
    configuration.max_message_size = 16777216;
    identifier = fdn_rtc.create_peer(&configuration);
    if (identifier < 0) return FDN_RTC_FAILED;
    peer = calloc(1, sizeof(*peer));
    if (peer == NULL) {
        fdn_rtc.delete_peer(identifier);
        return FDN_RTC_FAILED;
    }
    peer->identifier = identifier;
    atomic_init(&peer->references, 1);
    atomic_init(&peer->closed, 0);
    atomic_init(&peer->gathering, 0);
    atomic_flag_clear(&peer->lock);
    fdn_rtc.set_user_pointer(identifier, peer);
    if (fdn_rtc.set_gathering_callback(identifier,
                                       fdn_rtc_gathering_changed) < 0 ||
        fdn_rtc.set_data_channel_callback(identifier,
                                          fdn_rtc_data_channel) < 0) {
        fdn_rtc_peer_clear_callbacks(peer);
        fdn_rtc.delete_peer(identifier);
        free(peer);
        return FDN_RTC_FAILED;
    }
    *result = (uint64_t)(uintptr_t)peer;
    return FDN_RTC_OK;
}

uint64_t remolo_rtc_peer_retain(uint64_t handle) {
    fdn_rtc_peer *peer = (fdn_rtc_peer *)(uintptr_t)handle;
    return fdn_rtc_peer_retain(peer) ? handle : 0;
}

void remolo_rtc_peer_close(uint64_t *handle) {
    fdn_rtc_peer *peer;
    fdn_rtc_channel *incoming = NULL;
    if (handle == NULL || *handle == 0) return;
    peer = (fdn_rtc_peer *)(uintptr_t)*handle;
    *handle = 0;
    if (!atomic_exchange_explicit(&peer->closed, 1, memory_order_acq_rel)) {
        fdn_rtc_peer_clear_callbacks(peer);
        fdn_rtc.close_peer(peer->identifier);
        fdn_rtc_lock(&peer->lock);
        incoming = peer->incoming;
        peer->incoming = NULL;
        fdn_rtc_unlock(&peer->lock);
        fdn_rtc_channel_release(incoming);
    }
    fdn_rtc_peer_release(peer);
}

void remolo_rtc_peer_release(uint64_t *handle) {
    fdn_rtc_peer *peer;
    if (handle == NULL || *handle == 0) return;
    peer = (fdn_rtc_peer *)(uintptr_t)*handle;
    *handle = 0;
    fdn_rtc_peer_release(peer);
}

int32_t remolo_rtc_peer_set_remote(uint64_t handle, const fdn_string *sdp,
                                   const fdn_string *type) {
    fdn_rtc_peer *peer = (fdn_rtc_peer *)(uintptr_t)handle;
    char *sdp_text = NULL;
    char *type_text = NULL;
    int result;
    if (peer == NULL || !fdn_rtc_copy_string(sdp, &sdp_text) ||
        !fdn_rtc_copy_string(type, &type_text)) {
        free(sdp_text);
        free(type_text);
        return FDN_RTC_INVALID;
    }
    result = fdn_rtc.set_remote_description(peer->identifier, sdp_text,
                                            type_text);
    free(sdp_text);
    free(type_text);
    return result < 0 ? FDN_RTC_FAILED : FDN_RTC_OK;
}

int32_t remolo_rtc_peer_local(uint64_t handle, const fdn_string *type,
                              fdn_string *result) {
    fdn_rtc_peer *peer = (fdn_rtc_peer *)(uintptr_t)handle;
    char *type_text = NULL;
    char *sdp = NULL;
    int length;
    int status = FDN_RTC_FAILED;
    if (peer == NULL || !fdn_rtc_copy_string(type, &type_text)) {
        return FDN_RTC_INVALID;
    }
    atomic_store_explicit(&peer->gathering, 0, memory_order_release);
    if (fdn_rtc.set_local_description(peer->identifier, type_text) < 0) {
        goto cleanup;
    }
    while (!atomic_load_explicit(&peer->gathering, memory_order_acquire)) {
        if (atomic_load_explicit(&peer->closed, memory_order_acquire)) {
            status = FDN_RTC_CLOSED;
            goto cleanup;
        }
        fdn_rtc_pause();
    }
    length = fdn_rtc.get_local_description(peer->identifier, NULL, 0);
    if (length <= 0) goto cleanup;
    sdp = malloc((size_t)length);
    if (sdp == NULL ||
        fdn_rtc.get_local_description(peer->identifier, sdp, length) < 0) {
        goto cleanup;
    }
    status = fdn_rtc_output_string(sdp, result);

cleanup:
    free(sdp);
    free(type_text);
    return status;
}

int32_t remolo_rtc_channel_open(uint64_t handle, const fdn_string *label,
                                uint64_t *result) {
    fdn_rtc_peer *peer = (fdn_rtc_peer *)(uintptr_t)handle;
    fdn_rtc_channel *channel;
    char *label_text = NULL;
    int identifier;
    if (peer == NULL || result == NULL ||
        !fdn_rtc_copy_string(label, &label_text)) {
        return FDN_RTC_INVALID;
    }
    *result = 0;
    identifier = fdn_rtc.create_data_channel(peer->identifier, label_text);
    free(label_text);
    channel = fdn_rtc_channel_new(peer, identifier);
    if (channel == NULL) return FDN_RTC_FAILED;
    *result = (uint64_t)(uintptr_t)channel;
    return FDN_RTC_OK;
}

int32_t remolo_rtc_channel_accept(uint64_t handle, uint64_t *result) {
    fdn_rtc_peer *peer = (fdn_rtc_peer *)(uintptr_t)handle;
    fdn_rtc_channel *channel = NULL;
    if (peer == NULL || result == NULL) return FDN_RTC_INVALID;
    *result = 0;
    while (channel == NULL) {
        fdn_rtc_lock(&peer->lock);
        channel = peer->incoming;
        peer->incoming = NULL;
        fdn_rtc_unlock(&peer->lock);
        if (channel != NULL) break;
        if (atomic_load_explicit(&peer->closed, memory_order_acquire)) {
            return FDN_RTC_CLOSED;
        }
        fdn_rtc_pause();
    }
    *result = (uint64_t)(uintptr_t)channel;
    return FDN_RTC_OK;
}

int32_t remolo_rtc_channel_wait_open(uint64_t handle) {
    fdn_rtc_channel *channel = (fdn_rtc_channel *)(uintptr_t)handle;
    if (channel == NULL) return FDN_RTC_INVALID;
    while (!atomic_load_explicit(&channel->open, memory_order_acquire)) {
        if (atomic_load_explicit(&channel->closed, memory_order_acquire)) {
            return FDN_RTC_CLOSED;
        }
        fdn_rtc_pause();
    }
    return FDN_RTC_OK;
}

int32_t remolo_rtc_channel_split(uint64_t *handle, uint64_t *reader,
                                 uint64_t *writer, uint64_t *controller) {
    fdn_rtc_channel *channel;
    if (handle == NULL || *handle == 0 || reader == NULL || writer == NULL ||
        controller == NULL) {
        return FDN_RTC_INVALID;
    }
    channel = (fdn_rtc_channel *)(uintptr_t)*handle;
    atomic_fetch_add_explicit(&channel->references, 2, memory_order_relaxed);
    *reader = *handle;
    *writer = *handle;
    *controller = *handle;
    *handle = 0;
    return FDN_RTC_OK;
}

void remolo_rtc_channel_release(uint64_t *handle) {
    fdn_rtc_channel *channel;
    if (handle == NULL || *handle == 0) return;
    channel = (fdn_rtc_channel *)(uintptr_t)*handle;
    *handle = 0;
    fdn_rtc_channel_release(channel);
}

void remolo_rtc_channel_close(uint64_t *handle) {
    fdn_rtc_channel *channel;
    if (handle == NULL || *handle == 0) return;
    channel = (fdn_rtc_channel *)(uintptr_t)*handle;
    *handle = 0;
    if (!atomic_exchange_explicit(&channel->closed, 1, memory_order_acq_rel)) {
        fdn_rtc_channel_clear_callbacks(channel);
        fdn_rtc.close_channel(channel->identifier);
    }
    fdn_rtc_channel_release(channel);
}

int32_t remolo_rtc_channel_send(uint64_t handle, uint64_t value) {
    fdn_rtc_channel *channel = (fdn_rtc_channel *)(uintptr_t)handle;
    uint64_t length = 0;
    unsigned char *data = NULL;
    uint64_t byte = 0;
    uint64_t index;
    int result;
    if (channel == NULL || foundation_runtime_bytes_length(value, &length) != 0 ||
        length > INT32_MAX) {
        return FDN_RTC_INVALID;
    }
    if (length > 0) {
        data = malloc((size_t)length);
        if (data == NULL) return FDN_RTC_FAILED;
        for (index = 0; index < length; ++index) {
            if (foundation_runtime_bytes_at(value, index, &byte) != 0) {
                free(data);
                return FDN_RTC_FAILED;
            }
            data[index] = (unsigned char)byte;
        }
    }
    result = fdn_rtc.send_message(channel->identifier, (const char *)data,
                                  (int)length);
    free(data);
    return result < 0 ? FDN_RTC_FAILED : FDN_RTC_OK;
}

int32_t remolo_rtc_channel_receive(uint64_t handle, uint64_t *result) {
    fdn_rtc_channel *channel = (fdn_rtc_channel *)(uintptr_t)handle;
    fdn_rtc_message *message = NULL;
    int32_t status;
    if (channel == NULL || result == NULL) return FDN_RTC_INVALID;
    *result = 0;
    while (message == NULL) {
        fdn_rtc_lock(&channel->lock);
        message = channel->head;
        if (message != NULL) {
            channel->head = message->next;
            if (channel->head == NULL) channel->tail = NULL;
        }
        fdn_rtc_unlock(&channel->lock);
        if (message != NULL) break;
        if (atomic_load_explicit(&channel->closed, memory_order_acquire)) {
            return FDN_RTC_CLOSED;
        }
        fdn_rtc_pause();
    }
    status = fdn_rtc_output_bytes(message->data, message->length, result);
    free(message->data);
    free(message);
    return status;
}
