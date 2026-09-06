#if defined(__linux__) || defined(__APPLE__)
#ifndef _FILE_OFFSET_BITS
#define _FILE_OFFSET_BITS 64
#endif
#endif

#include "remolo_fuse.h"

#include "foundation/runtime.h"

#if defined(__linux__) || defined(__APPLE__)

#if defined(__APPLE__)
#define FUSE_USE_VERSION 26
#include <fuse/fuse.h>
#else
#define FUSE_USE_VERSION 31
#include <fuse3/fuse.h>
#endif

#include <errno.h>
#include <fcntl.h>
#include <pthread.h>
#include <stdbool.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>

enum remolo_fuse_operation {
    REMOLO_FUSE_GET_ATTRIBUTE = 1,
    REMOLO_FUSE_READ_DIRECTORY = 2,
    REMOLO_FUSE_OPEN = 3,
    REMOLO_FUSE_READ = 4,
    REMOLO_FUSE_WRITE = 5,
    REMOLO_FUSE_TRUNCATE = 6,
    REMOLO_FUSE_CREATE = 7,
    REMOLO_FUSE_MAKE_DIRECTORY = 8,
    REMOLO_FUSE_UNLINK = 9,
    REMOLO_FUSE_REMOVE_DIRECTORY = 10,
    REMOLO_FUSE_RENAME = 11
};

typedef struct remolo_fuse_bridge {
    pthread_mutex_t mutex;
    pthread_cond_t changed;
    pthread_t thread;
    char *mountpoint;
    bool thread_started;
    bool ready;
    bool stopped;
    bool pending;
    bool completed;
    int32_t status;
    uint32_t operation;
    char *path;
    char *second_path;
    uint64_t offset;
    uint64_t size;
    uint32_t mode;
    uint32_t flags;
    uint64_t data;
    char *read_buffer;
    size_t read_capacity;
    void *directory_buffer;
    fuse_fill_dir_t directory_filler;
    int callback_result;
} remolo_fuse_bridge;

static remolo_fuse_bridge *remolo_fuse_context(void) {
    struct fuse_context *context = fuse_get_context();
    return context == NULL ? NULL : context->private_data;
}

static char *remolo_fuse_copy_text(const char *value) {
    const char *source = value == NULL ? "" : value;
    const size_t length = strlen(source) + 1;
    char *copy = malloc(length);
    if (copy != NULL) {
        memcpy(copy, source, length);
    }
    return copy;
}

static void remolo_fuse_clear_event(remolo_fuse_bridge *bridge) {
    free(bridge->path);
    free(bridge->second_path);
    bridge->path = NULL;
    bridge->second_path = NULL;
    if (bridge->data != 0) {
        foundation_runtime_bytes_close(&bridge->data);
    }
    bridge->offset = 0;
    bridge->size = 0;
    bridge->mode = 0;
    bridge->flags = 0;
    bridge->read_buffer = NULL;
    bridge->read_capacity = 0;
    bridge->directory_buffer = NULL;
    bridge->directory_filler = NULL;
}

static int remolo_fuse_publish(uint32_t operation, const char *path,
                               const char *second_path, uint64_t offset,
                               uint64_t size, uint32_t mode, uint32_t flags,
                               uint64_t data, char *read_buffer,
                               size_t read_capacity, void *directory_buffer,
                               fuse_fill_dir_t directory_filler) {
    remolo_fuse_bridge *bridge = remolo_fuse_context();
    if (bridge == NULL) {
        foundation_runtime_bytes_close(&data);
        return -EIO;
    }
    pthread_mutex_lock(&bridge->mutex);
    if (bridge->stopped || bridge->pending) {
        pthread_mutex_unlock(&bridge->mutex);
        foundation_runtime_bytes_close(&data);
        return -EIO;
    }
    bridge->path = remolo_fuse_copy_text(path);
    bridge->second_path = remolo_fuse_copy_text(second_path);
    if (bridge->path == NULL || bridge->second_path == NULL) {
        remolo_fuse_clear_event(bridge);
        pthread_mutex_unlock(&bridge->mutex);
        foundation_runtime_bytes_close(&data);
        return -ENOMEM;
    }
    bridge->operation = operation;
    bridge->offset = offset;
    bridge->size = size;
    bridge->mode = mode;
    bridge->flags = flags;
    bridge->data = data;
    bridge->read_buffer = read_buffer;
    bridge->read_capacity = read_capacity;
    bridge->directory_buffer = directory_buffer;
    bridge->directory_filler = directory_filler;
    bridge->callback_result = -EIO;
    bridge->completed = false;
    bridge->pending = true;
    pthread_cond_broadcast(&bridge->changed);
    while (!bridge->completed && !bridge->stopped) {
        pthread_cond_wait(&bridge->changed, &bridge->mutex);
    }
    const int result = bridge->callback_result;
    remolo_fuse_clear_event(bridge);
    bridge->pending = false;
    bridge->completed = false;
    pthread_cond_broadcast(&bridge->changed);
    pthread_mutex_unlock(&bridge->mutex);
    return result;
}

#if defined(__APPLE__)
static void *remolo_fuse_init(struct fuse_conn_info *connection) {
#else
static void *remolo_fuse_init(struct fuse_conn_info *connection,
                              struct fuse_config *configuration) {
    configuration->kernel_cache = 0;
#endif
    (void)connection;
    remolo_fuse_bridge *bridge = remolo_fuse_context();
    if (bridge != NULL) {
        pthread_mutex_lock(&bridge->mutex);
        bridge->ready = true;
        pthread_cond_broadcast(&bridge->changed);
        pthread_mutex_unlock(&bridge->mutex);
    }
    return bridge;
}

#if defined(__APPLE__)
static int remolo_fuse_get_attribute(const char *path, struct stat *attribute) {
#else
static int remolo_fuse_get_attribute(const char *path, struct stat *attribute,
                                     struct fuse_file_info *info) {
    (void)info;
#endif
    return remolo_fuse_publish(REMOLO_FUSE_GET_ATTRIBUTE, path, NULL, 0, 0,
                               0, 0, 0, (char *)attribute,
                               sizeof(*attribute), NULL, NULL);
}

#if defined(__APPLE__)
static int remolo_fuse_read_directory(const char *path, void *buffer,
                                      fuse_fill_dir_t filler, off_t offset,
                                      struct fuse_file_info *info) {
#else
static int remolo_fuse_read_directory(const char *path, void *buffer,
                                      fuse_fill_dir_t filler, off_t offset,
                                      struct fuse_file_info *info,
                                      enum fuse_readdir_flags flags) {
    (void)flags;
#endif
    (void)offset;
    (void)info;
    return remolo_fuse_publish(REMOLO_FUSE_READ_DIRECTORY, path, NULL, 0, 0,
                               0, 0, 0, NULL, 0, buffer, filler);
}

static int remolo_fuse_open_file(const char *path,
                                 struct fuse_file_info *info) {
    const int access = info->flags & O_ACCMODE;
    const bool writes = access == O_WRONLY || access == O_RDWR ||
                        (info->flags & (O_TRUNC | O_APPEND)) != 0;
    return remolo_fuse_publish(REMOLO_FUSE_OPEN, path, NULL, 0, 0, 0,
                               writes ? 1U : 0U, 0, NULL, 0, NULL, NULL);
}

static int remolo_fuse_read_file(const char *path, char *buffer, size_t size,
                                 off_t offset, struct fuse_file_info *info) {
    (void)info;
    if (offset < 0) {
        return -EINVAL;
    }
    return remolo_fuse_publish(REMOLO_FUSE_READ, path, NULL,
                               (uint64_t)offset, (uint64_t)size, 0, 0, 0,
                               buffer, size, NULL, NULL);
}

static int remolo_fuse_write_file(const char *path, const char *buffer,
                                  size_t size, off_t offset,
                                  struct fuse_file_info *info) {
    uint64_t data = 0;
    (void)info;
    if (offset < 0 || foundation_runtime_bytes_copy_from_raw(
                          (const uint8_t *)buffer, (uint64_t)size, &data) != 0) {
        return -EINVAL;
    }
    return remolo_fuse_publish(REMOLO_FUSE_WRITE, path, NULL,
                               (uint64_t)offset, (uint64_t)size, 0, 0, data,
                               NULL, 0, NULL, NULL);
}

#if defined(__APPLE__)
static int remolo_fuse_truncate_file(const char *path, off_t size) {
#else
static int remolo_fuse_truncate_file(const char *path, off_t size,
                                     struct fuse_file_info *info) {
    (void)info;
#endif
    if (size < 0) {
        return -EINVAL;
    }
    return remolo_fuse_publish(REMOLO_FUSE_TRUNCATE, path, NULL, 0,
                               (uint64_t)size, 0, 0, 0, NULL, 0, NULL, NULL);
}

static int remolo_fuse_create_file(const char *path, mode_t mode,
                                   struct fuse_file_info *info) {
    return remolo_fuse_publish(REMOLO_FUSE_CREATE, path, NULL, 0, 0,
                               (uint32_t)mode, (uint32_t)info->flags, 0,
                               NULL, 0, NULL, NULL);
}

static int remolo_fuse_make_directory(const char *path, mode_t mode) {
    return remolo_fuse_publish(REMOLO_FUSE_MAKE_DIRECTORY, path, NULL, 0, 0,
                               (uint32_t)mode, 0, 0, NULL, 0, NULL, NULL);
}

static int remolo_fuse_unlink_path(const char *path) {
    return remolo_fuse_publish(REMOLO_FUSE_UNLINK, path, NULL, 0, 0, 0, 0,
                               0, NULL, 0, NULL, NULL);
}

static int remolo_fuse_remove_directory(const char *path) {
    return remolo_fuse_publish(REMOLO_FUSE_REMOVE_DIRECTORY, path, NULL, 0,
                               0, 0, 0, 0, NULL, 0, NULL, NULL);
}

#if defined(__APPLE__)
static int remolo_fuse_rename_path(const char *source,
                                   const char *destination) {
#else
static int remolo_fuse_rename_path(const char *source, const char *destination,
                                   unsigned int flags) {
    if (flags != 0) {
        return -EINVAL;
    }
#endif
    return remolo_fuse_publish(REMOLO_FUSE_RENAME, source, destination, 0, 0,
                               0, 0, 0, NULL, 0, NULL, NULL);
}

static struct fuse_operations remolo_fuse_operations = {
    .init = remolo_fuse_init,
    .getattr = remolo_fuse_get_attribute,
    .readdir = remolo_fuse_read_directory,
    .open = remolo_fuse_open_file,
    .read = remolo_fuse_read_file,
    .write = remolo_fuse_write_file,
    .truncate = remolo_fuse_truncate_file,
    .create = remolo_fuse_create_file,
    .mkdir = remolo_fuse_make_directory,
    .unlink = remolo_fuse_unlink_path,
    .rmdir = remolo_fuse_remove_directory,
    .rename = remolo_fuse_rename_path,
};

static void *remolo_fuse_run(void *value) {
    remolo_fuse_bridge *bridge = value;
    char *arguments[] = {"remolo", "-f", "-s", bridge->mountpoint, NULL};
    const int status = fuse_main_real(4, arguments, &remolo_fuse_operations,
                                      sizeof(remolo_fuse_operations), bridge);
    pthread_mutex_lock(&bridge->mutex);
    bridge->status = status;
    bridge->stopped = true;
    pthread_cond_broadcast(&bridge->changed);
    pthread_mutex_unlock(&bridge->mutex);
    return NULL;
}

uint64_t remolo_fuse_open(const fdn_string *mountpoint) {
    remolo_fuse_bridge *bridge;
    if (mountpoint == NULL || mountpoint->length == 0 ||
        mountpoint->data == NULL ||
        memchr(mountpoint->data, '\0', mountpoint->length) != NULL) {
        return 0;
    }
    bridge = calloc(1, sizeof(*bridge));
    if (bridge == NULL) {
        return 0;
    }
    bridge->mountpoint = malloc(mountpoint->length + 1);
    if (bridge->mountpoint != NULL) {
        memcpy(bridge->mountpoint, mountpoint->data, mountpoint->length);
        bridge->mountpoint[mountpoint->length] = '\0';
    }
    if (bridge->mountpoint == NULL || pthread_mutex_init(&bridge->mutex, NULL) != 0 ||
        pthread_cond_init(&bridge->changed, NULL) != 0) {
        free(bridge->mountpoint);
        free(bridge);
        return 0;
    }
    if (pthread_create(&bridge->thread, NULL, remolo_fuse_run, bridge) != 0) {
        pthread_cond_destroy(&bridge->changed);
        pthread_mutex_destroy(&bridge->mutex);
        free(bridge->mountpoint);
        free(bridge);
        return 0;
    }
    bridge->thread_started = true;
    pthread_mutex_lock(&bridge->mutex);
    while (!bridge->ready && !bridge->stopped) {
        pthread_cond_wait(&bridge->changed, &bridge->mutex);
    }
    const bool ready = bridge->ready;
    pthread_mutex_unlock(&bridge->mutex);
    if (!ready) {
        uint64_t handle = (uint64_t)(uintptr_t)bridge;
        (void)remolo_fuse_close(&handle);
        return 0;
    }
    return (uint64_t)(uintptr_t)bridge;
}

int32_t remolo_fuse_next(uint64_t handle, uint32_t *operation,
                         void *path_value, void *second_path_value,
                         uint64_t *offset, uint64_t *size, uint32_t *mode,
                         uint32_t *flags, uint64_t *data) {
    remolo_fuse_bridge *bridge = (remolo_fuse_bridge *)(uintptr_t)handle;
    fdn_string *path = path_value;
    fdn_string *second_path = second_path_value;
    if (bridge == NULL || operation == NULL || path == NULL ||
        second_path == NULL || offset == NULL || size == NULL || mode == NULL ||
        flags == NULL || data == NULL) {
        return -1;
    }
    pthread_mutex_lock(&bridge->mutex);
    while (!bridge->pending && !bridge->stopped) {
        pthread_cond_wait(&bridge->changed, &bridge->mutex);
    }
    if (!bridge->pending) {
        const int32_t status = bridge->status;
        pthread_mutex_unlock(&bridge->mutex);
        return status == 0 ? 0 : -1;
    }
    fdn_string_drop(path);
    fdn_string_drop(second_path);
    const fdn_string path_source = {bridge->path, strlen(bridge->path), 0};
    const fdn_string second_source = {
        bridge->second_path, strlen(bridge->second_path), 0};
    *path = foundation_runtime_string_copy(&path_source);
    *second_path = foundation_runtime_string_copy(&second_source);
    *operation = bridge->operation;
    *offset = bridge->offset;
    *size = bridge->size;
    *mode = bridge->mode;
    *flags = bridge->flags;
    *data = bridge->data;
    bridge->data = 0;
    pthread_mutex_unlock(&bridge->mutex);
    return 1;
}

int32_t remolo_fuse_add_directory_entry(uint64_t handle, const fdn_string *name,
                                        uint32_t mode) {
    remolo_fuse_bridge *bridge = (remolo_fuse_bridge *)(uintptr_t)handle;
    struct stat attribute;
    char *native_name;
    int result;
    if (bridge == NULL || name == NULL ||
        (name->length != 0 && name->data == NULL) ||
        (name->length != 0 &&
         memchr(name->data, '\0', name->length) != NULL)) {
        return -1;
    }
    native_name = malloc(name->length + 1);
    if (native_name == NULL) {
        return -1;
    }
    memcpy(native_name, name->data, name->length);
    native_name[name->length] = '\0';
    pthread_mutex_lock(&bridge->mutex);
    if (!bridge->pending ||
        bridge->operation != REMOLO_FUSE_READ_DIRECTORY ||
        bridge->directory_filler == NULL) {
        pthread_mutex_unlock(&bridge->mutex);
        free(native_name);
        return -1;
    }
    memset(&attribute, 0, sizeof(attribute));
    attribute.st_mode = (mode_t)mode;
#if defined(__APPLE__)
    result = bridge->directory_filler(bridge->directory_buffer, native_name,
                                      &attribute, 0);
#else
    result = bridge->directory_filler(bridge->directory_buffer, native_name,
                                      &attribute, 0, 0);
#endif
    pthread_mutex_unlock(&bridge->mutex);
    free(native_name);
    return result == 0 ? 0 : 1;
}

static int32_t remolo_fuse_complete(remolo_fuse_bridge *bridge, int result) {
    if (bridge == NULL) {
        return -1;
    }
    pthread_mutex_lock(&bridge->mutex);
    if (!bridge->pending || bridge->completed) {
        pthread_mutex_unlock(&bridge->mutex);
        return -1;
    }
    bridge->callback_result = result;
    bridge->completed = true;
    pthread_cond_broadcast(&bridge->changed);
    pthread_mutex_unlock(&bridge->mutex);
    return 0;
}

int32_t remolo_fuse_reply_error(uint64_t handle, int32_t error) {
    remolo_fuse_bridge *bridge = (remolo_fuse_bridge *)(uintptr_t)handle;
    return remolo_fuse_complete(bridge, error > 0 ? -error : error);
}

int32_t remolo_fuse_reply_ok(uint64_t handle) {
    return remolo_fuse_complete((remolo_fuse_bridge *)(uintptr_t)handle, 0);
}

int32_t remolo_fuse_reply_attribute(uint64_t handle, uint32_t mode,
                                    uint64_t size, uint64_t modified) {
    remolo_fuse_bridge *bridge = (remolo_fuse_bridge *)(uintptr_t)handle;
    struct stat *attribute;
    if (bridge == NULL) {
        return -1;
    }
    pthread_mutex_lock(&bridge->mutex);
    if (!bridge->pending ||
        bridge->operation != REMOLO_FUSE_GET_ATTRIBUTE ||
        bridge->read_buffer == NULL ||
        bridge->read_capacity < sizeof(*attribute)) {
        pthread_mutex_unlock(&bridge->mutex);
        return -1;
    }
    attribute = (struct stat *)bridge->read_buffer;
    memset(attribute, 0, sizeof(*attribute));
    attribute->st_mode = (mode_t)mode;
    attribute->st_nlink = S_ISDIR(mode) ? 2 : 1;
    attribute->st_size = (off_t)size;
    attribute->st_mtime = (time_t)modified;
    attribute->st_ctime = (time_t)modified;
    attribute->st_atime = (time_t)modified;
    bridge->callback_result = 0;
    bridge->completed = true;
    pthread_cond_broadcast(&bridge->changed);
    pthread_mutex_unlock(&bridge->mutex);
    return 0;
}

int32_t remolo_fuse_reply_read(uint64_t handle, uint64_t data) {
    remolo_fuse_bridge *bridge = (remolo_fuse_bridge *)(uintptr_t)handle;
    uint64_t length = 0;
    int32_t result = -1;
    if (bridge == NULL ||
        foundation_runtime_bytes_length(data, &length) != 0) {
        return -1;
    }
    pthread_mutex_lock(&bridge->mutex);
    if (bridge->pending && bridge->operation == REMOLO_FUSE_READ &&
        length <= bridge->read_capacity &&
        foundation_runtime_bytes_copy_to_raw(
            data, (uint8_t *)bridge->read_buffer,
            (uint64_t)bridge->read_capacity) == 0) {
        bridge->callback_result = (int)length;
        bridge->completed = true;
        pthread_cond_broadcast(&bridge->changed);
        result = 0;
    }
    pthread_mutex_unlock(&bridge->mutex);
    return result;
}

int32_t remolo_fuse_reply_write(uint64_t handle, uint64_t count) {
    if (count > INT32_MAX) {
        return -1;
    }
    return remolo_fuse_complete((remolo_fuse_bridge *)(uintptr_t)handle,
                                (int)count);
}

int32_t remolo_fuse_close(uint64_t *handle) {
    remolo_fuse_bridge *bridge;
    if (handle == NULL) {
        return -1;
    }
    bridge = (remolo_fuse_bridge *)(uintptr_t)*handle;
    *handle = 0;
    if (bridge == NULL) {
        return 0;
    }
    if (bridge->thread_started) {
        pthread_join(bridge->thread, NULL);
    }
    remolo_fuse_clear_event(bridge);
    pthread_cond_destroy(&bridge->changed);
    pthread_mutex_destroy(&bridge->mutex);
    free(bridge->mountpoint);
    free(bridge);
    return 0;
}

#else

uint64_t remolo_fuse_open(const fdn_string *mountpoint) {
    (void)mountpoint;
    return 0;
}

int32_t remolo_fuse_next(uint64_t handle, uint32_t *operation,
                         void *path, void *second_path, uint64_t *offset,
                         uint64_t *size, uint32_t *mode, uint32_t *flags,
                         uint64_t *data) {
    (void)handle;
    (void)operation;
    (void)path;
    (void)second_path;
    (void)offset;
    (void)size;
    (void)mode;
    (void)flags;
    (void)data;
    return -1;
}

int32_t remolo_fuse_add_directory_entry(uint64_t handle, const fdn_string *name,
                                        uint32_t mode) {
    (void)handle;
    (void)name;
    (void)mode;
    return -1;
}

int32_t remolo_fuse_reply_error(uint64_t handle, int32_t error) {
    (void)handle;
    (void)error;
    return -1;
}

int32_t remolo_fuse_reply_ok(uint64_t handle) {
    (void)handle;
    return -1;
}

int32_t remolo_fuse_reply_attribute(uint64_t handle, uint32_t mode,
                                    uint64_t size, uint64_t modified) {
    (void)handle;
    (void)mode;
    (void)size;
    (void)modified;
    return -1;
}

int32_t remolo_fuse_reply_read(uint64_t handle, uint64_t data) {
    (void)handle;
    (void)data;
    return -1;
}

int32_t remolo_fuse_reply_write(uint64_t handle, uint64_t count) {
    (void)handle;
    (void)count;
    return -1;
}

int32_t remolo_fuse_close(uint64_t *handle) {
    if (handle != NULL) {
        *handle = 0;
    }
    return 0;
}

#endif
