#include <foundation/runtime.h>

#include <SDL3/SDL.h>

#include <stdbool.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

#define NK_INCLUDE_COMMAND_USERDATA
#define NK_INCLUDE_DEFAULT_FONT
#define NK_INCLUDE_FONT_BAKING
#define NK_INCLUDE_STANDARD_VARARGS
#define NK_INCLUDE_VERTEX_BUFFER_OUTPUT
#define NK_BUTTON_TRIGGER_ON_RELEASE

#define NK_INT8 Sint8
#define NK_UINT8 Uint8
#define NK_INT16 Sint16
#define NK_UINT16 Uint16
#define NK_INT32 Sint32
#define NK_UINT32 Uint32
#define NK_SIZE_TYPE uintptr_t
#define NK_POINTER_TYPE uintptr_t
#define NK_BOOL bool
#define NK_ASSERT(condition) SDL_assert(condition)
#define NK_STATIC_ASSERT(exp) SDL_COMPILE_TIME_ASSERT(, exp)
#define NK_MEMSET(dst, c, len) SDL_memset(dst, c, len)
#define NK_MEMCPY(dst, src, len) SDL_memcpy(dst, src, len)
#define NK_VSNPRINTF(s, n, f, a) SDL_vsnprintf(s, n, f, a)
#define NK_STRTOD(str, endptr) SDL_strtod(str, endptr)
#define NK_INV_SQRT(f) (1.0f / SDL_sqrtf(f))
#define NK_SIN(f) SDL_sinf(f)
#define NK_COS(f) SDL_cosf(f)

static char *remolo_ui_dtoa(char *destination, double value) {
    (void)SDL_snprintf(destination, 64, "%.17g", value);
    return destination;
}

#define NK_DTOA(str, value) remolo_ui_dtoa(str, value)

#if defined(__GNUC__)
#pragma GCC diagnostic push
#pragma GCC diagnostic ignored "-Wconversion"
#pragma GCC diagnostic ignored "-Wdouble-promotion"
#pragma GCC diagnostic ignored "-Wsign-conversion"
#endif
#if defined(__clang__)
#pragma clang diagnostic ignored "-Wc23-extensions"
#pragma clang diagnostic ignored "-Wstrict-prototypes"
#endif
#if defined(_MSC_VER)
#pragma warning(push)
#pragma warning(disable : 4116 4244 4267 4701 5287)
#endif
#define NK_IMPLEMENTATION
#include "vendor/nuklear.h"
#define NK_SDL3_RENDERER_IMPLEMENTATION
#include "vendor/nuklear_sdl3_renderer.h"
#if defined(_MSC_VER)
#pragma warning(pop)
#endif
#if defined(__GNUC__)
#pragma GCC diagnostic pop
#endif

#include "vendor/inter_regular.inc"
#include "vendor/inter_semibold.inc"
#include "vendor/remolo_icon.inc"

enum remolo_ui_status {
    REMOLO_UI_OK = 0,
    REMOLO_UI_UNAVAILABLE = 1,
    REMOLO_UI_INVALID = 2,
    REMOLO_UI_FAILED = 3,
};

enum remolo_ui_input_kind {
    REMOLO_UI_INPUT_MOUSE_MOVE = 0,
    REMOLO_UI_INPUT_MOUSE_BUTTON = 1,
    REMOLO_UI_INPUT_KEY = 2,
    REMOLO_UI_INPUT_WHEEL = 3,
};

typedef struct remolo_ui_input {
    uint64_t kind;
    uint64_t x;
    uint64_t y;
    uint64_t button;
    uint64_t key;
    int64_t delta;
    bool down;
} remolo_ui_input;

#define REMOLO_UI_INPUT_CAPACITY 512
#define REMOLO_UI_EDIT_CAPACITY 8
#define REMOLO_UI_EDIT_NAME_CAPACITY 48

typedef struct remolo_ui_edit_state {
    char name[REMOLO_UI_EDIT_NAME_CAPACITY];
    uint64_t name_length;
    char *buffer;
    uint64_t capacity;
} remolo_ui_edit_state;

typedef struct remolo_ui {
    SDL_Window *window;
    SDL_Renderer *renderer;
    struct nk_context *context;
    struct nk_font *regular_font;
    struct nk_font *heading_font;
    SDL_Texture *logo_texture;
    SDL_Texture *remote_texture;
    uint8_t *remote_pixels;
    uint64_t remote_capacity;
    uint64_t remote_width;
    uint64_t remote_height;
    struct nk_rect remote_bounds;
    remolo_ui_input input_queue[REMOLO_UI_INPUT_CAPACITY];
    remolo_ui_edit_state edit_states[REMOLO_UI_EDIT_CAPACITY];
    uint64_t input_head;
    uint64_t input_count;
    uint64_t edit_count;
    int shape_width;
    int shape_height;
    bool remote_bounds_valid;
    bool remote_control;
    bool remote_keys[256];
    bool remote_buttons[3];
    bool shape_maximized;
    char tooltip[128];
    uint64_t tooltip_length;
    struct nk_rect tooltip_anchor;
    struct nk_color background;
    struct nk_color panel;
    struct nk_color raised;
    struct nk_color text;
    struct nk_color muted;
    struct nk_color accent;
    bool closing;
} remolo_ui;

static remolo_ui_edit_state *remolo_ui_edit_for(remolo_ui *ui,
                                                 const fdn_string *name,
                                                 uint64_t capacity) {
    remolo_ui_edit_state *state;
    char *buffer;
    uint64_t index;
    for (index = 0; index < ui->edit_count; index++) {
        state = &ui->edit_states[index];
        if (state->name_length == name->length &&
            SDL_memcmp(state->name, name->data, name->length) == 0) {
            if (state->capacity >= capacity) return state;
            buffer = SDL_realloc(state->buffer, (size_t)capacity);
            if (buffer == NULL) return NULL;
            SDL_memset(buffer + state->capacity, 0,
                       (size_t)(capacity - state->capacity));
            state->buffer = buffer;
            state->capacity = capacity;
            return state;
        }
    }
    if (ui->edit_count == REMOLO_UI_EDIT_CAPACITY) return NULL;
    state = &ui->edit_states[ui->edit_count];
    buffer = SDL_calloc((size_t)capacity, 1);
    if (buffer == NULL) return NULL;
    SDL_memcpy(state->name, name->data, name->length);
    state->name[name->length] = '\0';
    state->name_length = name->length;
    state->buffer = buffer;
    state->capacity = capacity;
    ui->edit_count++;
    return state;
}

static bool remolo_ui_enqueue_input(remolo_ui *ui, remolo_ui_input input) {
    uint64_t index;
    if (input.kind == REMOLO_UI_INPUT_MOUSE_MOVE && ui->input_count != 0) {
        index = (ui->input_head + ui->input_count - 1) % REMOLO_UI_INPUT_CAPACITY;
        if (ui->input_queue[index].kind == REMOLO_UI_INPUT_MOUSE_MOVE) {
            ui->input_queue[index] = input;
            return true;
        }
    }
    if (ui->input_count == REMOLO_UI_INPUT_CAPACITY) return false;
    index = (ui->input_head + ui->input_count) % REMOLO_UI_INPUT_CAPACITY;
    ui->input_queue[index] = input;
    ui->input_count++;
    return true;
}

static bool remolo_ui_remote_point(const remolo_ui *ui, float x, float y,
                                   uint64_t *remote_x, uint64_t *remote_y) {
    float normalized_x;
    float normalized_y;
    if (!ui->remote_bounds_valid || ui->remote_bounds.w <= 0.0f ||
        ui->remote_bounds.h <= 0.0f) {
        return false;
    }
    normalized_x = (x - ui->remote_bounds.x) / ui->remote_bounds.w;
    normalized_y = (y - ui->remote_bounds.y) / ui->remote_bounds.h;
    if (normalized_x < 0.0f) normalized_x = 0.0f;
    if (normalized_x > 1.0f) normalized_x = 1.0f;
    if (normalized_y < 0.0f) normalized_y = 0.0f;
    if (normalized_y > 1.0f) normalized_y = 1.0f;
    *remote_x = (uint64_t)(normalized_x * 32767.0f + 0.5f);
    *remote_y = (uint64_t)(normalized_y * 32767.0f + 0.5f);
    return true;
}

static bool remolo_ui_inside_remote(const remolo_ui *ui, float x, float y) {
    return ui->remote_bounds_valid && x >= ui->remote_bounds.x &&
           y >= ui->remote_bounds.y && x < ui->remote_bounds.x + ui->remote_bounds.w &&
           y < ui->remote_bounds.y + ui->remote_bounds.h;
}

static uint64_t remolo_ui_linux_key(SDL_Scancode scancode) {
    switch (scancode) {
    case SDL_SCANCODE_A: return 30;
    case SDL_SCANCODE_B: return 48;
    case SDL_SCANCODE_C: return 46;
    case SDL_SCANCODE_D: return 32;
    case SDL_SCANCODE_E: return 18;
    case SDL_SCANCODE_F: return 33;
    case SDL_SCANCODE_G: return 34;
    case SDL_SCANCODE_H: return 35;
    case SDL_SCANCODE_I: return 23;
    case SDL_SCANCODE_J: return 36;
    case SDL_SCANCODE_K: return 37;
    case SDL_SCANCODE_L: return 38;
    case SDL_SCANCODE_M: return 50;
    case SDL_SCANCODE_N: return 49;
    case SDL_SCANCODE_O: return 24;
    case SDL_SCANCODE_P: return 25;
    case SDL_SCANCODE_Q: return 16;
    case SDL_SCANCODE_R: return 19;
    case SDL_SCANCODE_S: return 31;
    case SDL_SCANCODE_T: return 20;
    case SDL_SCANCODE_U: return 22;
    case SDL_SCANCODE_V: return 47;
    case SDL_SCANCODE_W: return 17;
    case SDL_SCANCODE_X: return 45;
    case SDL_SCANCODE_Y: return 21;
    case SDL_SCANCODE_Z: return 44;
    case SDL_SCANCODE_1: return 2;
    case SDL_SCANCODE_2: return 3;
    case SDL_SCANCODE_3: return 4;
    case SDL_SCANCODE_4: return 5;
    case SDL_SCANCODE_5: return 6;
    case SDL_SCANCODE_6: return 7;
    case SDL_SCANCODE_7: return 8;
    case SDL_SCANCODE_8: return 9;
    case SDL_SCANCODE_9: return 10;
    case SDL_SCANCODE_0: return 11;
    case SDL_SCANCODE_RETURN: return 28;
    case SDL_SCANCODE_ESCAPE: return 1;
    case SDL_SCANCODE_BACKSPACE: return 14;
    case SDL_SCANCODE_TAB: return 15;
    case SDL_SCANCODE_SPACE: return 57;
    case SDL_SCANCODE_MINUS: return 12;
    case SDL_SCANCODE_EQUALS: return 13;
    case SDL_SCANCODE_LEFTBRACKET: return 26;
    case SDL_SCANCODE_RIGHTBRACKET: return 27;
    case SDL_SCANCODE_BACKSLASH: return 43;
    case SDL_SCANCODE_NONUSBACKSLASH: return 86;
    case SDL_SCANCODE_SEMICOLON: return 39;
    case SDL_SCANCODE_APOSTROPHE: return 40;
    case SDL_SCANCODE_GRAVE: return 41;
    case SDL_SCANCODE_COMMA: return 51;
    case SDL_SCANCODE_PERIOD: return 52;
    case SDL_SCANCODE_SLASH: return 53;
    case SDL_SCANCODE_CAPSLOCK: return 58;
    case SDL_SCANCODE_F1: return 59;
    case SDL_SCANCODE_F2: return 60;
    case SDL_SCANCODE_F3: return 61;
    case SDL_SCANCODE_F4: return 62;
    case SDL_SCANCODE_F5: return 63;
    case SDL_SCANCODE_F6: return 64;
    case SDL_SCANCODE_F7: return 65;
    case SDL_SCANCODE_F8: return 66;
    case SDL_SCANCODE_F9: return 67;
    case SDL_SCANCODE_F10: return 68;
    case SDL_SCANCODE_F11: return 87;
    case SDL_SCANCODE_F12: return 88;
    case SDL_SCANCODE_INSERT: return 110;
    case SDL_SCANCODE_HOME: return 102;
    case SDL_SCANCODE_PAGEUP: return 104;
    case SDL_SCANCODE_DELETE: return 111;
    case SDL_SCANCODE_END: return 107;
    case SDL_SCANCODE_PAGEDOWN: return 109;
    case SDL_SCANCODE_RIGHT: return 106;
    case SDL_SCANCODE_LEFT: return 105;
    case SDL_SCANCODE_DOWN: return 108;
    case SDL_SCANCODE_UP: return 103;
    case SDL_SCANCODE_KP_DIVIDE: return 98;
    case SDL_SCANCODE_KP_MULTIPLY: return 55;
    case SDL_SCANCODE_KP_MINUS: return 74;
    case SDL_SCANCODE_KP_PLUS: return 78;
    case SDL_SCANCODE_KP_ENTER: return 96;
    case SDL_SCANCODE_LCTRL: return 29;
    case SDL_SCANCODE_LSHIFT: return 42;
    case SDL_SCANCODE_LALT: return 56;
    case SDL_SCANCODE_LGUI: return 125;
    case SDL_SCANCODE_RCTRL: return 97;
    case SDL_SCANCODE_RSHIFT: return 54;
    case SDL_SCANCODE_RALT: return 100;
    case SDL_SCANCODE_RGUI: return 126;
    default: return 0;
    }
}

static uint64_t remolo_ui_linux_button(Uint8 button) {
    if (button == SDL_BUTTON_LEFT) return 272;
    if (button == SDL_BUTTON_RIGHT) return 273;
    if (button == SDL_BUTTON_MIDDLE) return 274;
    return 0;
}

static void remolo_ui_release_remote(remolo_ui *ui) {
    uint64_t key;
    uint64_t button;
    const uint64_t buttons[] = {272, 273, 274};
    if (!ui->remote_control) return;
    for (key = 1; key < 256; key++) {
        if (!ui->remote_keys[key]) continue;
        (void)remolo_ui_enqueue_input(ui, (remolo_ui_input){
            .kind = REMOLO_UI_INPUT_KEY, .key = key, .down = false});
        ui->remote_keys[key] = false;
    }
    for (button = 0; button < 3; button++) {
        if (!ui->remote_buttons[button]) continue;
        (void)remolo_ui_enqueue_input(ui, (remolo_ui_input){
            .kind = REMOLO_UI_INPUT_MOUSE_BUTTON,
            .button = buttons[button],
            .down = false});
        ui->remote_buttons[button] = false;
    }
    ui->remote_control = false;
    (void)SDL_CaptureMouse(false);
}

static bool remolo_ui_handle_remote_event(remolo_ui *ui, const SDL_Event *event) {
    uint64_t x;
    uint64_t y;
    uint64_t key;
    uint64_t button;
    uint64_t button_index;
    int64_t delta;
    if (event->type == SDL_EVENT_KEY_DOWN && ui->remote_control &&
        event->key.scancode == SDL_SCANCODE_F8) {
        remolo_ui_release_remote(ui);
        return true;
    }
    if (event->type == SDL_EVENT_MOUSE_BUTTON_DOWN && !ui->remote_control &&
        remolo_ui_inside_remote(ui, event->button.x, event->button.y)) {
        ui->remote_control = true;
        (void)SDL_CaptureMouse(true);
    }
    if (!ui->remote_control) return false;
    if (event->type == SDL_EVENT_MOUSE_MOTION) {
        if (remolo_ui_remote_point(ui, event->motion.x, event->motion.y, &x, &y)) {
            (void)remolo_ui_enqueue_input(ui, (remolo_ui_input){
                .kind = REMOLO_UI_INPUT_MOUSE_MOVE, .x = x, .y = y});
        }
        return true;
    }
    if (event->type == SDL_EVENT_MOUSE_BUTTON_DOWN ||
        event->type == SDL_EVENT_MOUSE_BUTTON_UP) {
        button = remolo_ui_linux_button(event->button.button);
        if (button == 0) return true;
        button_index = button - 272;
        ui->remote_buttons[button_index] = event->button.down;
        if (remolo_ui_remote_point(ui, event->button.x, event->button.y, &x, &y)) {
            (void)remolo_ui_enqueue_input(ui, (remolo_ui_input){
                .kind = REMOLO_UI_INPUT_MOUSE_MOVE, .x = x, .y = y});
        }
        (void)remolo_ui_enqueue_input(ui, (remolo_ui_input){
            .kind = REMOLO_UI_INPUT_MOUSE_BUTTON,
            .button = button,
            .down = event->button.down});
        return true;
    }
    if (event->type == SDL_EVENT_MOUSE_WHEEL) {
        delta = event->wheel.integer_y;
        if (delta == 0 && event->wheel.y != 0.0f) {
            delta = event->wheel.y > 0.0f ? 1 : -1;
        }
        if (delta != 0) {
            (void)remolo_ui_enqueue_input(ui, (remolo_ui_input){
                .kind = REMOLO_UI_INPUT_WHEEL, .delta = delta});
        }
        return true;
    }
    if (event->type == SDL_EVENT_KEY_DOWN || event->type == SDL_EVENT_KEY_UP) {
        if (event->key.repeat) return true;
        key = remolo_ui_linux_key(event->key.scancode);
        if (key == 0) return true;
        ui->remote_keys[key] = event->key.down;
        (void)remolo_ui_enqueue_input(ui, (remolo_ui_input){
            .kind = REMOLO_UI_INPUT_KEY, .key = key, .down = event->key.down});
        return true;
    }
    return false;
}

static remolo_ui *remolo_ui_from(uint64_t handle) {
    return (remolo_ui *)(uintptr_t)handle;
}

static char *remolo_ui_text(const fdn_string *value) {
    char *copy;
    if (value == NULL || (value->data == NULL && value->length != 0) ||
        memchr(value->data, '\0', value->length) != NULL) {
        return NULL;
    }
    if (value->length == SIZE_MAX) return NULL;
    copy = SDL_malloc(value->length + 1);
    if (copy == NULL) return NULL;
    if (value->length != 0) SDL_memcpy(copy, value->data, value->length);
    copy[value->length] = '\0';
    return copy;
}

static void remolo_ui_copy_selection(struct nk_context *context,
                                     struct nk_text_edit *edit) {
    nk_rune rune;
    int glyph_length;
    const int begin = NK_MIN(edit->select_start, edit->select_end);
    const int end = NK_MAX(edit->select_start, edit->select_end);
    const char *text;
    if (begin == end || context->clip.copy == NULL) return;
    text = nk_str_at_const(&edit->string, begin, &rune, &glyph_length);
    context->clip.copy(context->clip.userdata, text, end - begin);
}

static void remolo_ui_sync_edit(struct nk_window *window,
                                struct nk_text_edit *edit, char *buffer,
                                uint64_t capacity) {
    const nk_size length = edit->string.buffer.allocated;
    buffer[NK_MIN(length, (nk_size)capacity - 1)] = '\0';
    window->edit.cursor = edit->cursor;
    window->edit.sel_start = edit->select_start;
    window->edit.sel_end = edit->select_end;
    window->edit.mode = edit->mode;
    window->edit.scrollbar.x = (nk_uint)edit->scrollbar.x;
    window->edit.scrollbar.y = (nk_uint)edit->scrollbar.y;
}

static void remolo_ui_edit_menu(remolo_ui *ui, struct nk_window *window,
                                struct nk_rect bounds, char *buffer,
                                uint64_t capacity) {
    struct nk_text_edit *edit = &ui->context->text_edit;
    const bool selected = edit->select_start != edit->select_end;
    const bool paste_available = SDL_HasClipboardText();
    bool changed = false;
    if (!nk_contextual_begin(ui->context, 0, nk_vec2(172.0f, 152.0f), bounds)) {
        return;
    }
    nk_layout_row_dynamic(ui->context, 32.0f, 1);
    if (!selected) {
        nk_widget_disable_begin(ui->context);
        ui->context->style.contextual_button.color_factor_background = 1.0f;
    }
    if (nk_contextual_item_label(ui->context, "Cut", NK_TEXT_LEFT)) {
        remolo_ui_copy_selection(ui->context, edit);
        changed = nk_textedit_cut(edit);
    }
    if (nk_contextual_item_label(ui->context, "Copy", NK_TEXT_LEFT)) {
        remolo_ui_copy_selection(ui->context, edit);
    }
    if (!selected) nk_widget_disable_end(ui->context);
    if (!paste_available) {
        nk_widget_disable_begin(ui->context);
        ui->context->style.contextual_button.color_factor_background = 1.0f;
    }
    if (nk_contextual_item_label(ui->context, "Paste", NK_TEXT_LEFT)) {
        ui->context->clip.paste(ui->context->clip.userdata, edit);
        changed = true;
    }
    if (!paste_available) nk_widget_disable_end(ui->context);
    if (nk_contextual_item_label(ui->context, "Select all", NK_TEXT_LEFT)) {
        nk_textedit_select_all(edit);
        changed = true;
    }
    if (changed) remolo_ui_sync_edit(window, edit, buffer, capacity);
    nk_contextual_end(ui->context);
}

static void remolo_ui_apply_theme(remolo_ui *ui, bool light) {
    struct nk_color table[NK_COLOR_COUNT];
    const struct nk_color background = light ? nk_rgb(247, 249, 252) : nk_rgb(15, 24, 40);
    const struct nk_color panel = light ? nk_rgb(255, 255, 255) : nk_rgb(18, 30, 49);
    const struct nk_color raised = light ? nk_rgb(235, 239, 245) : nk_rgb(25, 40, 64);
    const struct nk_color border = light ? nk_rgb(197, 207, 221) : nk_rgb(47, 66, 94);
    const struct nk_color text = light ? nk_rgb(15, 24, 40) : nk_rgb(247, 249, 252);
    const struct nk_color muted = light ? nk_rgb(75, 91, 113) : nk_rgb(156, 172, 195);
    const struct nk_color accent = nk_rgb(95, 221, 122);

    table[NK_COLOR_TEXT] = text;
    table[NK_COLOR_WINDOW] = background;
    table[NK_COLOR_HEADER] = panel;
    table[NK_COLOR_BORDER] = border;
    table[NK_COLOR_BUTTON] = raised;
    table[NK_COLOR_BUTTON_HOVER] = border;
    table[NK_COLOR_BUTTON_ACTIVE] = accent;
    table[NK_COLOR_TOGGLE] = raised;
    table[NK_COLOR_TOGGLE_HOVER] = border;
    table[NK_COLOR_TOGGLE_CURSOR] = accent;
    table[NK_COLOR_SELECT] = raised;
    table[NK_COLOR_SELECT_ACTIVE] = accent;
    table[NK_COLOR_SLIDER] = raised;
    table[NK_COLOR_SLIDER_CURSOR] = muted;
    table[NK_COLOR_SLIDER_CURSOR_HOVER] = text;
    table[NK_COLOR_SLIDER_CURSOR_ACTIVE] = accent;
    table[NK_COLOR_PROPERTY] = panel;
    table[NK_COLOR_EDIT] = panel;
    table[NK_COLOR_EDIT_CURSOR] = text;
    table[NK_COLOR_COMBO] = panel;
    table[NK_COLOR_CHART] = panel;
    table[NK_COLOR_CHART_COLOR] = muted;
    table[NK_COLOR_CHART_COLOR_HIGHLIGHT] = accent;
    table[NK_COLOR_SCROLLBAR] = background;
    table[NK_COLOR_SCROLLBAR_CURSOR] = raised;
    table[NK_COLOR_SCROLLBAR_CURSOR_HOVER] = border;
    table[NK_COLOR_SCROLLBAR_CURSOR_ACTIVE] = muted;
    table[NK_COLOR_TAB_HEADER] = panel;
    nk_style_from_table(ui->context, table);

    ui->context->style.window.padding = nk_vec2(24.0f, 0.0f);
    ui->context->style.window.spacing = nk_vec2(8.0f, 8.0f);
    ui->context->style.window.border = 0.0f;
    ui->context->style.window.rounding = 8.0f;
    ui->context->style.window.group_padding = nk_vec2(16.0f, 16.0f);
    ui->context->style.window.contextual_border = 1.0f;
    ui->context->style.window.contextual_border_color = border;
    ui->context->style.window.contextual_padding = nk_vec2(6.0f, 6.0f);
    ui->context->style.button.rounding = 6.0f;
    ui->context->style.button.padding = nk_vec2(12.0f, 8.0f);
    ui->context->style.contextual_button.normal = nk_style_item_color(background);
    ui->context->style.contextual_button.hover = nk_style_item_color(raised);
    ui->context->style.contextual_button.active = nk_style_item_color(accent);
    ui->context->style.contextual_button.border_color = border;
    ui->context->style.contextual_button.text_background = background;
    ui->context->style.contextual_button.text_normal = text;
    ui->context->style.contextual_button.text_hover = text;
    ui->context->style.contextual_button.text_active = background;
    ui->context->style.contextual_button.border = 0.0f;
    ui->context->style.contextual_button.rounding = 5.0f;
    ui->context->style.contextual_button.padding = nk_vec2(10.0f, 7.0f);
    ui->context->style.contextual_button.disabled_factor = 0.48f;
    ui->context->style.edit.rounding = 6.0f;
    ui->context->style.edit.border = 1.0f;
    ui->context->style.edit.padding = nk_vec2(12.0f, 8.0f);
    ui->background = background;
    ui->panel = panel;
    ui->raised = raised;
    ui->text = text;
    ui->muted = muted;
    ui->accent = accent;
}

static SDL_Texture *remolo_ui_logo_texture(SDL_Renderer *renderer) {
    SDL_Texture *texture;
    if (remolo_icon_rgba_len != 64U * 64U * 4U) return NULL;
    texture = SDL_CreateTexture(renderer, SDL_PIXELFORMAT_RGBA32,
                                SDL_TEXTUREACCESS_STATIC, 64, 64);
    if (texture == NULL) return NULL;
    if (!SDL_UpdateTexture(texture, NULL, remolo_icon_rgba, 64 * 4) ||
        !SDL_SetTextureBlendMode(texture, SDL_BLENDMODE_BLEND)) {
        SDL_DestroyTexture(texture);
        return NULL;
    }
    return texture;
}

static bool remolo_ui_set_window_shape(remolo_ui *ui) {
    SDL_Surface *shape;
    const SDL_PixelFormatDetails *format;
    Uint32 transparent;
    Uint32 opaque;
    int width;
    int height;
    const int radius = 10;
    const bool maximized =
        (SDL_GetWindowFlags(ui->window) & SDL_WINDOW_MAXIMIZED) != 0;
    if (!SDL_GetWindowSize(ui->window, &width, &height)) return false;
    if (width == ui->shape_width && height == ui->shape_height &&
        maximized == ui->shape_maximized) {
        return true;
    }
    shape = SDL_CreateSurface(width, height, SDL_PIXELFORMAT_RGBA32);
    if (shape == NULL) return false;
    format = SDL_GetPixelFormatDetails(shape->format);
    if (format == NULL) {
        SDL_DestroySurface(shape);
        return false;
    }
    transparent = SDL_MapRGBA(format, NULL, 0, 0, 0, 0);
    opaque = SDL_MapRGBA(format, NULL, 255, 255, 255, 255);
    if (!SDL_FillSurfaceRect(shape, NULL, opaque) || !SDL_LockSurface(shape)) {
        SDL_DestroySurface(shape);
        return false;
    }
    if (!maximized) {
        for (int y = 0; y < radius; y++) {
            Uint32 *top = (Uint32 *)((uint8_t *)shape->pixels + y * shape->pitch);
            Uint32 *bottom =
                (Uint32 *)((uint8_t *)shape->pixels + (height - 1 - y) * shape->pitch);
            for (int x = 0; x < radius; x++) {
                const int dx = radius - 1 - x;
                const int dy = radius - 1 - y;
                if (dx * dx + dy * dy < radius * radius) continue;
                top[x] = transparent;
                top[width - 1 - x] = transparent;
                bottom[x] = transparent;
                bottom[width - 1 - x] = transparent;
            }
        }
    }
    SDL_UnlockSurface(shape);
    const bool applied = SDL_SetWindowShape(ui->window, shape);
    SDL_DestroySurface(shape);
    if (!applied) return false;
    ui->shape_width = width;
    ui->shape_height = height;
    ui->shape_maximized = maximized;
    return true;
}

static SDL_HitTestResult remolo_ui_hit_test(SDL_Window *window,
                                            const SDL_Point *area,
                                            void *data) {
    int width;
    int height;
    const int edge = 6;
    (void)data;
    if (!SDL_GetWindowSize(window, &width, &height)) return SDL_HITTEST_NORMAL;
    if ((SDL_GetWindowFlags(window) & SDL_WINDOW_MAXIMIZED) != 0) {
        if (area->y < 40 && area->x < width - 108) return SDL_HITTEST_DRAGGABLE;
        return SDL_HITTEST_NORMAL;
    }
    const bool left = area->x < edge;
    const bool right = area->x >= width - edge;
    const bool top = area->y < edge;
    const bool bottom = area->y >= height - edge;
    if (left && top) return SDL_HITTEST_RESIZE_TOPLEFT;
    if (right && top) return SDL_HITTEST_RESIZE_TOPRIGHT;
    if (left && bottom) return SDL_HITTEST_RESIZE_BOTTOMLEFT;
    if (right && bottom) return SDL_HITTEST_RESIZE_BOTTOMRIGHT;
    if (left) return SDL_HITTEST_RESIZE_LEFT;
    if (right) return SDL_HITTEST_RESIZE_RIGHT;
    if (top) return SDL_HITTEST_RESIZE_TOP;
    if (bottom) return SDL_HITTEST_RESIZE_BOTTOM;
    if (area->y < 40 && area->x < width - 108) return SDL_HITTEST_DRAGGABLE;
    return SDL_HITTEST_NORMAL;
}

static bool remolo_ui_set_window_icon(SDL_Window *window) {
    SDL_Surface *surface;
    surface = SDL_CreateSurfaceFrom(64, 64, SDL_PIXELFORMAT_RGBA32,
                                    (void *)remolo_icon_rgba, 64 * 4);
    if (surface == NULL) return false;
    const bool applied = SDL_SetWindowIcon(window, surface);
    SDL_DestroySurface(surface);
    return applied;
}

int32_t remolo_ui_open(const fdn_string *title, uint64_t width, uint64_t height,
                       uint64_t *handle) {
    remolo_ui *ui;
    struct nk_font_atlas *atlas;
    struct nk_font_config regular_config;
    struct nk_font_config heading_config;
    char *window_title;

    if (title == NULL || handle == NULL || width < 720 || height < 480 ||
        width > INT32_MAX || height > INT32_MAX) {
        return REMOLO_UI_INVALID;
    }
    window_title = remolo_ui_text(title);
    if (window_title == NULL) return REMOLO_UI_INVALID;
    if (!SDL_Init(SDL_INIT_VIDEO | SDL_INIT_EVENTS)) {
        SDL_free(window_title);
        return REMOLO_UI_UNAVAILABLE;
    }
    ui = SDL_calloc(1, sizeof(*ui));
    if (ui == NULL) {
        SDL_free(window_title);
        SDL_Quit();
        return REMOLO_UI_FAILED;
    }
    if (!SDL_CreateWindowAndRenderer(window_title, (int)width, (int)height,
                                     SDL_WINDOW_BORDERLESS | SDL_WINDOW_RESIZABLE |
                                         SDL_WINDOW_HIGH_PIXEL_DENSITY |
                                         SDL_WINDOW_TRANSPARENT,
                                     &ui->window, &ui->renderer)) {
        SDL_free(window_title);
        SDL_free(ui);
        SDL_Quit();
        return REMOLO_UI_UNAVAILABLE;
    }
    SDL_free(window_title);
    if (!SDL_SetWindowHitTest(ui->window, remolo_ui_hit_test, ui)) {
        SDL_DestroyRenderer(ui->renderer);
        SDL_DestroyWindow(ui->window);
        SDL_free(ui);
        SDL_Quit();
        return REMOLO_UI_FAILED;
    }
    if (!remolo_ui_set_window_icon(ui->window)) {
        SDL_DestroyRenderer(ui->renderer);
        SDL_DestroyWindow(ui->window);
        SDL_free(ui);
        SDL_Quit();
        return REMOLO_UI_FAILED;
    }
    if (!remolo_ui_set_window_shape(ui)) {
        SDL_DestroyRenderer(ui->renderer);
        SDL_DestroyWindow(ui->window);
        SDL_free(ui);
        SDL_Quit();
        return REMOLO_UI_FAILED;
    }
    (void)SDL_SetRenderVSync(ui->renderer, 1);
    ui->context = nk_sdl_init(ui->window, ui->renderer, nk_sdl_allocator());
    atlas = nk_sdl_font_stash_begin(ui->context);
    regular_config = nk_font_config(17.0f);
    regular_config.ttf_data_owned_by_atlas = 0;
    heading_config = nk_font_config(28.0f);
    heading_config.ttf_data_owned_by_atlas = 0;
    ui->regular_font = nk_font_atlas_add_from_memory(
        atlas, (void *)remolo_inter_regular_ttf, remolo_inter_regular_ttf_len,
        17.0f, &regular_config);
    ui->heading_font = nk_font_atlas_add_from_memory(
        atlas, (void *)remolo_inter_semibold_ttf, remolo_inter_semibold_ttf_len,
        28.0f, &heading_config);
    if (ui->regular_font == NULL || ui->heading_font == NULL) {
        nk_sdl_shutdown(ui->context);
        SDL_DestroyRenderer(ui->renderer);
        SDL_DestroyWindow(ui->window);
        SDL_free(ui);
        SDL_Quit();
        return REMOLO_UI_FAILED;
    }
    atlas->default_font = ui->regular_font;
    nk_sdl_font_stash_end(ui->context);
    ui->logo_texture = remolo_ui_logo_texture(ui->renderer);
    if (ui->logo_texture == NULL) {
        nk_sdl_shutdown(ui->context);
        SDL_DestroyRenderer(ui->renderer);
        SDL_DestroyWindow(ui->window);
        SDL_free(ui);
        SDL_Quit();
        return REMOLO_UI_FAILED;
    }
    remolo_ui_apply_theme(ui, false);
    *handle = (uint64_t)(uintptr_t)ui;
    return REMOLO_UI_OK;
}

void remolo_ui_close(uint64_t *handle) {
    remolo_ui *ui;
    uint64_t index;
    if (handle == NULL || *handle == 0) return;
    ui = remolo_ui_from(*handle);
    for (index = 0; index < ui->edit_count; index++) {
        SDL_free(ui->edit_states[index].buffer);
    }
    SDL_free(ui->remote_pixels);
    SDL_DestroyTexture(ui->remote_texture);
    SDL_DestroyTexture(ui->logo_texture);
    nk_sdl_shutdown(ui->context);
    SDL_DestroyRenderer(ui->renderer);
    SDL_DestroyWindow(ui->window);
    SDL_free(ui);
    SDL_Quit();
    *handle = 0;
}

int32_t remolo_ui_begin_frame(uint64_t handle) {
    remolo_ui *ui = remolo_ui_from(handle);
    SDL_Event event;
    bool resized = false;
    if (ui == NULL) return REMOLO_UI_INVALID;
    nk_input_begin(ui->context);
    while (SDL_PollEvent(&event)) {
        if (event.type == SDL_EVENT_QUIT ||
            (event.type == SDL_EVENT_WINDOW_CLOSE_REQUESTED &&
             SDL_GetWindowFromEvent(&event) == ui->window)) {
            ui->closing = true;
        }
        if ((event.type == SDL_EVENT_WINDOW_RESIZED ||
             event.type == SDL_EVENT_WINDOW_MAXIMIZED ||
             event.type == SDL_EVENT_WINDOW_RESTORED) &&
            SDL_GetWindowFromEvent(&event) == ui->window) {
            resized = true;
        }
        if (event.type == SDL_EVENT_WINDOW_FOCUS_LOST &&
            SDL_GetWindowFromEvent(&event) == ui->window) {
            remolo_ui_release_remote(ui);
        }
        if (!remolo_ui_handle_remote_event(ui, &event)) {
            (void)nk_sdl_handle_event(ui->context, &event);
        }
    }
    nk_input_end(ui->context);
    ui->remote_bounds_valid = false;
    ui->tooltip_length = 0;
    if (resized && !remolo_ui_set_window_shape(ui)) return REMOLO_UI_FAILED;
    return ui->closing ? 1 : REMOLO_UI_OK;
}

int32_t remolo_ui_end_frame(uint64_t handle) {
    remolo_ui *ui = remolo_ui_from(handle);
    if (ui == NULL) return REMOLO_UI_INVALID;
    if (!SDL_SetRenderDrawColor(ui->renderer, ui->background.r, ui->background.g,
                                ui->background.b, ui->background.a) ||
        !SDL_RenderClear(ui->renderer)) {
        return REMOLO_UI_FAILED;
    }
    nk_sdl_render(ui->context, NK_ANTI_ALIASING_ON);
    if (!SDL_RenderPresent(ui->renderer)) return REMOLO_UI_FAILED;
    nk_sdl_update_TextInput(ui->context);
    return REMOLO_UI_OK;
}

int32_t remolo_ui_set_theme(uint64_t handle, uint64_t theme) {
    remolo_ui *ui = remolo_ui_from(handle);
    if (ui == NULL || theme > 1) return REMOLO_UI_INVALID;
    remolo_ui_apply_theme(ui, theme == 1);
    return REMOLO_UI_OK;
}

int32_t remolo_ui_size(uint64_t handle, uint64_t *width, uint64_t *height) {
    remolo_ui *ui = remolo_ui_from(handle);
    int window_width;
    int window_height;
    if (ui == NULL || width == NULL || height == NULL) return REMOLO_UI_INVALID;
    if (!SDL_GetWindowSize(ui->window, &window_width, &window_height) ||
        window_width < 0 || window_height < 0) {
        return REMOLO_UI_FAILED;
    }
    *width = (uint64_t)window_width;
    *height = (uint64_t)window_height;
    return REMOLO_UI_OK;
}

bool remolo_ui_begin_root(uint64_t handle) {
    remolo_ui *ui = remolo_ui_from(handle);
    int width;
    int height;
    if (ui == NULL || !SDL_GetWindowSize(ui->window, &width, &height)) return false;
    return nk_begin(ui->context, "remolo",
                    nk_rect(0.0f, 0.0f, (float)width, (float)height),
                    NK_WINDOW_BACKGROUND | NK_WINDOW_NO_SCROLLBAR);
}

void remolo_ui_end_root(uint64_t handle) {
    remolo_ui *ui = remolo_ui_from(handle);
    if (ui != NULL) nk_end(ui->context);
}

static bool remolo_ui_window_button(remolo_ui *ui, struct nk_command_buffer *canvas,
                                    struct nk_rect bounds, uint64_t icon) {
    nk_flags state = 0;
    const bool pressed = nk_button_behavior(&state, bounds, &ui->context->input,
                                            NK_BUTTON_DEFAULT);
    const struct nk_color foreground =
        (state & NK_WIDGET_STATE_HOVER) != 0 ? ui->text : ui->muted;
    const float x = bounds.x + bounds.w * 0.5f;
    const float y = bounds.y + bounds.h * 0.5f;
    if (icon == 0) {
        nk_stroke_line(canvas, x - 4.0f, y + 3.0f, x + 4.0f, y + 3.0f, 1.25f,
                       foreground);
    } else if (icon == 1) {
        nk_stroke_line(canvas, x - 5.0f, y - 1.0f, x - 5.0f, y - 5.0f, 1.25f,
                       foreground);
        nk_stroke_line(canvas, x - 5.0f, y - 5.0f, x - 1.0f, y - 5.0f, 1.25f,
                       foreground);
        nk_stroke_line(canvas, x + 5.0f, y + 1.0f, x + 5.0f, y + 5.0f, 1.25f,
                       foreground);
        nk_stroke_line(canvas, x + 1.0f, y + 5.0f, x + 5.0f, y + 5.0f, 1.25f,
                       foreground);
    } else {
        nk_stroke_line(canvas, x - 4.0f, y - 4.0f, x + 4.0f, y + 4.0f, 1.25f,
                       foreground);
        nk_stroke_line(canvas, x + 4.0f, y - 4.0f, x - 4.0f, y + 4.0f, 1.25f,
                       foreground);
    }
    return pressed;
}

void remolo_ui_titlebar(uint64_t handle) {
    remolo_ui *ui = remolo_ui_from(handle);
    int width;
    int height;
    struct nk_command_buffer *canvas;
    struct nk_rect clip;
    if (ui == NULL || !SDL_GetWindowSize(ui->window, &width, &height)) return;
    (void)height;
    canvas = nk_window_get_canvas(ui->context);
    clip = canvas->clip;
    nk_push_scissor(canvas, nk_rect(0.0f, 0.0f, (float)width, (float)height));
    if (remolo_ui_window_button(
            ui, canvas, nk_rect((float)width - 108.0f, 0.0f, 36.0f, 36.0f), 0)) {
        (void)SDL_MinimizeWindow(ui->window);
    }
    if (remolo_ui_window_button(
            ui, canvas, nk_rect((float)width - 72.0f, 0.0f, 36.0f, 36.0f), 1)) {
        if ((SDL_GetWindowFlags(ui->window) & SDL_WINDOW_MAXIMIZED) != 0) {
            (void)SDL_RestoreWindow(ui->window);
        } else {
            (void)SDL_MaximizeWindow(ui->window);
        }
    }
    if (remolo_ui_window_button(
            ui, canvas, nk_rect((float)width - 36.0f, 0.0f, 36.0f, 36.0f), 2)) {
        ui->closing = true;
    }
    if ((SDL_GetWindowFlags(ui->window) & SDL_WINDOW_MAXIMIZED) == 0) {
        nk_stroke_rect(
            canvas,
            nk_rect(0.5f, 0.5f, (float)width - 1.0f, (float)height - 1.0f),
            10.0f, 1.0f, ui->context->style.window.border_color);
    }
    if (ui->tooltip_length != 0) {
        const struct nk_user_font *font = ui->context->style.font;
        const float measured = font->width(
            font->userdata, font->height, ui->tooltip, (int)ui->tooltip_length);
        struct nk_rect bounds = nk_rect(
            ui->tooltip_anchor.x + ui->tooltip_anchor.w + 8.0f,
            ui->tooltip_anchor.y + (ui->tooltip_anchor.h - 26.0f) * 0.5f,
            measured + 16.0f,
            26.0f);
        if (bounds.x + bounds.w > (float)width - 8.0f) {
            bounds.x = ui->tooltip_anchor.x - bounds.w - 8.0f;
        }
        nk_fill_rect(canvas, bounds, 5.0f, ui->raised);
        nk_draw_text(canvas,
                     nk_rect(bounds.x + 8.0f, bounds.y + 4.0f,
                             measured, 18.0f),
                     ui->tooltip, (int)ui->tooltip_length, font,
                     nk_rgba(0, 0, 0, 0), ui->text);
    }
    nk_push_scissor(canvas, clip);
}

void remolo_ui_row(uint64_t handle, float height, uint64_t columns) {
    remolo_ui *ui = remolo_ui_from(handle);
    if (ui != NULL && columns > 0 && columns <= INT32_MAX) {
        nk_layout_row_dynamic(ui->context, height, (int)columns);
    }
}

void remolo_ui_row_begin(uint64_t handle, float height, uint64_t columns) {
    remolo_ui *ui = remolo_ui_from(handle);
    if (ui != NULL && columns > 0 && columns <= INT32_MAX) {
        nk_layout_row_begin(ui->context, NK_DYNAMIC, height, (int)columns);
    }
}

void remolo_ui_row_push(uint64_t handle, float ratio) {
    remolo_ui *ui = remolo_ui_from(handle);
    if (ui != NULL) nk_layout_row_push(ui->context, ratio);
}

void remolo_ui_row_end(uint64_t handle) {
    remolo_ui *ui = remolo_ui_from(handle);
    if (ui != NULL) nk_layout_row_end(ui->context);
}

bool remolo_ui_begin_group(uint64_t handle, const fdn_string *name, bool scrollable) {
    remolo_ui *ui = remolo_ui_from(handle);
    char *group_name;
    bool visible;
    if (ui == NULL) return false;
    group_name = remolo_ui_text(name);
    if (group_name == NULL) return false;
    visible = nk_group_begin(ui->context, group_name,
                             scrollable ? 0 : NK_WINDOW_NO_SCROLLBAR);
    SDL_free(group_name);
    return visible;
}

void remolo_ui_end_group(uint64_t handle) {
    remolo_ui *ui = remolo_ui_from(handle);
    if (ui != NULL) nk_group_end(ui->context);
}

void remolo_ui_space(uint64_t handle, float height) {
    remolo_ui *ui = remolo_ui_from(handle);
    if (ui != NULL) {
        nk_layout_row_dynamic(ui->context, height, 1);
        nk_spacing(ui->context, 1);
    }
}

void remolo_ui_empty(uint64_t handle) {
    remolo_ui *ui = remolo_ui_from(handle);
    if (ui != NULL) nk_spacing(ui->context, 1);
}

void remolo_ui_separator(uint64_t handle) {
    remolo_ui *ui = remolo_ui_from(handle);
    struct nk_rect bounds;
    struct nk_command_buffer *canvas;
    if (ui == NULL) return;
    nk_layout_row_dynamic(ui->context, 1.0f, 1);
    if (nk_widget(&bounds, ui->context) == NK_WIDGET_INVALID) return;
    canvas = nk_window_get_canvas(ui->context);
    nk_stroke_line(canvas, bounds.x, bounds.y, bounds.x + bounds.w, bounds.y, 1.0f,
                   ui->context->style.window.border_color);
}

void remolo_ui_brand(uint64_t handle) {
    remolo_ui *ui = remolo_ui_from(handle);
    struct nk_rect bounds;
    struct nk_command_buffer *canvas;
    struct nk_image image;
    if (ui == NULL) return;
    if (nk_widget(&bounds, ui->context) == NK_WIDGET_INVALID) return;
    canvas = nk_window_get_canvas(ui->context);
    image = nk_image_ptr(ui->logo_texture);
    bounds.x += (bounds.w - 40.0f) * 0.5f;
    bounds.y += (bounds.h - 40.0f) * 0.5f;
    bounds.w = 40.0f;
    bounds.h = 40.0f;
    nk_draw_image(canvas, bounds, &image, nk_rgb(255, 255, 255));
}

static void remolo_ui_draw_icon(struct nk_command_buffer *canvas, struct nk_rect bounds,
                                uint64_t icon, struct nk_color color) {
    const float x = bounds.x + bounds.w * 0.5f;
    const float y = bounds.y + bounds.h * 0.5f;
    const float left = x - 9.0f;
    const float right = x + 9.0f;
    const float top = y - 8.0f;
    const float bottom = y + 8.0f;
    if (icon == 0) {
        nk_stroke_circle(canvas, nk_rect(left, y - 4.0f, 9.0f, 9.0f), 2.0f, color);
        nk_stroke_circle(canvas, nk_rect(x, y - 4.0f, 9.0f, 9.0f), 2.0f, color);
        nk_stroke_line(canvas, x - 4.0f, y, x + 4.0f, y, 2.0f, color);
    } else if (icon == 1) {
        nk_stroke_rect(canvas, nk_rect(left, y, 18.0f, 10.0f), 2.0f, 2.0f, color);
        nk_stroke_line(canvas, x, top, x, y + 3.0f, 2.0f, color);
        nk_stroke_line(canvas, x, top, x - 5.0f, top + 5.0f, 2.0f, color);
        nk_stroke_line(canvas, x, top, x + 5.0f, top + 5.0f, 2.0f, color);
    } else if (icon == 2) {
        nk_stroke_rect(canvas, nk_rect(left, top, 18.0f, 16.0f), 3.0f, 2.0f, color);
        nk_stroke_line(canvas, left + 4.0f, y - 3.0f, left + 8.0f, y, 2.0f, color);
        nk_stroke_line(canvas, left + 8.0f, y, left + 4.0f, y + 3.0f, 2.0f, color);
        nk_stroke_line(canvas, x + 1.0f, y + 4.0f, right - 3.0f, y + 4.0f, 2.0f, color);
    } else if (icon == 3) {
        const float points[] = {
            left, top + 3.0f, x - 2.0f, top + 3.0f, x + 1.0f, top + 6.0f,
            right, top + 6.0f, right, bottom, left, bottom, left, top + 3.0f
        };
        nk_stroke_polyline(canvas, points, 7, 2.0f, color);
    } else if (icon == 4) {
        nk_stroke_rect(canvas, nk_rect(left, top, 18.0f, 13.0f), 2.0f, 2.0f, color);
        nk_stroke_line(canvas, x, y + 5.0f, x, bottom, 2.0f, color);
        nk_stroke_line(canvas, x - 6.0f, bottom, x + 6.0f, bottom, 2.0f, color);
    } else {
        nk_stroke_circle(canvas, nk_rect(x - 6.0f, y - 6.0f, 12.0f, 12.0f), 2.0f, color);
        nk_stroke_line(canvas, x, top - 2.0f, x, top + 1.0f, 2.0f, color);
        nk_stroke_line(canvas, x, bottom - 1.0f, x, bottom + 2.0f, 2.0f, color);
        nk_stroke_line(canvas, left - 2.0f, y, left + 1.0f, y, 2.0f, color);
        nk_stroke_line(canvas, right - 1.0f, y, right + 2.0f, y, 2.0f, color);
    }
}

bool remolo_ui_icon_button(uint64_t handle, uint64_t icon, const fdn_string *label,
                           bool selected, bool enabled) {
    remolo_ui *ui = remolo_ui_from(handle);
    struct nk_rect bounds;
    struct nk_command_buffer *canvas;
    nk_flags state = 0;
    struct nk_color foreground;
    bool pressed = false;
    if (ui == NULL || label == NULL || icon > 5) return false;
    if (nk_widget(&bounds, ui->context) == NK_WIDGET_INVALID) return false;
    if (enabled) {
        pressed = nk_button_behavior(&state, bounds, &ui->context->input, NK_BUTTON_DEFAULT);
    }
    canvas = nk_window_get_canvas(ui->context);
    if (selected) {
        nk_fill_rect(canvas, bounds, 8.0f, ui->accent);
        foreground = nk_rgb(9, 12, 18);
    } else if ((state & NK_WIDGET_STATE_HOVER) != 0) {
        nk_fill_rect(canvas, bounds, 8.0f, ui->raised);
        foreground = ui->text;
    } else {
        foreground = enabled ? ui->muted : nk_rgba(91, 105, 125, 110);
    }
    remolo_ui_draw_icon(canvas, bounds, icon, foreground);
    if ((state & NK_WIDGET_STATE_HOVER) != 0) {
        ui->tooltip_length = label->length < sizeof(ui->tooltip)
                                 ? label->length
                                 : sizeof(ui->tooltip) - 1;
        SDL_memcpy(ui->tooltip, label->data, (size_t)ui->tooltip_length);
        ui->tooltip[ui->tooltip_length] = '\0';
        ui->tooltip_anchor = bounds;
    }
    return pressed;
}

void remolo_ui_heading(uint64_t handle, const fdn_string *value) {
    remolo_ui *ui = remolo_ui_from(handle);
    const struct nk_user_font *previous;
    if (ui == NULL || value == NULL || value->length > INT32_MAX) return;
    previous = ui->context->style.font;
    nk_style_set_font(ui->context, &ui->heading_font->handle);
    nk_text(ui->context, value->data, (int)value->length, NK_TEXT_LEFT);
    nk_style_set_font(ui->context, previous);
}

void remolo_ui_label(uint64_t handle, const fdn_string *value, uint64_t tone, bool wrap) {
    remolo_ui *ui = remolo_ui_from(handle);
    struct nk_color previous;
    struct nk_color selected;
    if (ui == NULL || value == NULL || value->length > INT32_MAX) return;
    previous = ui->context->style.text.color;
    selected = previous;
    if (tone == 1) selected = nk_rgb(156, 172, 195);
    if (tone == 2) selected = nk_rgb(95, 221, 122);
    if (tone == 3) selected = nk_rgb(255, 112, 112);
    ui->context->style.text.color = selected;
    if (wrap) {
        nk_text_wrap(ui->context, value->data, (int)value->length);
    } else {
        nk_text(ui->context, value->data, (int)value->length, NK_TEXT_LEFT);
    }
    ui->context->style.text.color = previous;
}

void remolo_ui_terminal_text(uint64_t handle, const fdn_string *value,
                             float minimum_height) {
    remolo_ui *ui = remolo_ui_from(handle);
    const struct nk_user_font *font;
    struct nk_command_buffer *canvas;
    struct nk_rect bounds;
    uint64_t start = 0;
    uint64_t end;
    uint64_t index;
    uint64_t lines = 1;
    float line_height;
    float height;
    float y;
    if (ui == NULL || value == NULL || value->length > INT32_MAX) return;
    for (index = 0; index < value->length; index++) {
        if (value->data[index] == '\n') lines++;
    }
    font = ui->context->style.font;
    line_height = font->height + 4.0f;
    height = (float)lines * line_height + 12.0f;
    if (height < minimum_height) height = minimum_height;
    nk_layout_row_dynamic(ui->context, height, 1);
    if (nk_widget(&bounds, ui->context) == NK_WIDGET_INVALID) return;
    canvas = nk_window_get_canvas(ui->context);
    y = bounds.y + 6.0f;
    for (index = 0; index <= value->length; index++) {
        if (index != value->length && value->data[index] != '\n') continue;
        end = index;
        if (end > start && value->data[end - 1] == '\r') end--;
        if (end > start) {
            nk_draw_text(canvas,
                         nk_rect(bounds.x + 8.0f, y, bounds.w - 16.0f,
                                 line_height),
                         value->data + start, (int)(end - start), font,
                         nk_rgba(0, 0, 0, 0), ui->text);
        }
        y += line_height;
        start = index + 1;
    }
}

bool remolo_ui_button(uint64_t handle, const fdn_string *value, bool selected,
                      bool primary) {
    remolo_ui *ui = remolo_ui_from(handle);
    struct nk_style_button previous;
    bool pressed;
    if (ui == NULL || value == NULL || value->length > INT32_MAX) return false;
    previous = ui->context->style.button;
    if (primary || selected) {
        ui->context->style.button.normal = nk_style_item_color(nk_rgb(95, 221, 122));
        ui->context->style.button.hover = nk_style_item_color(nk_rgb(111, 235, 138));
        ui->context->style.button.active = nk_style_item_color(nk_rgb(75, 193, 101));
        ui->context->style.button.text_normal = nk_rgb(9, 12, 18);
        ui->context->style.button.text_hover = nk_rgb(9, 12, 18);
        ui->context->style.button.text_active = nk_rgb(9, 12, 18);
    }
    pressed = nk_button_text(ui->context, value->data, (int)value->length);
    ui->context->style.button = previous;
    return pressed;
}

bool remolo_ui_file_entry(uint64_t handle, const fdn_string *name,
                          const fdn_string *details, bool directory) {
    remolo_ui *ui = remolo_ui_from(handle);
    struct nk_rect bounds;
    struct nk_rect name_bounds;
    struct nk_rect details_bounds;
    struct nk_rect icon_bounds;
    struct nk_command_buffer *canvas;
    nk_flags state = 0;
    struct nk_color foreground;
    bool pressed;
    if (ui == NULL || name == NULL || details == NULL || name->length > INT32_MAX ||
        details->length > INT32_MAX) {
        return false;
    }
    if (nk_widget(&bounds, ui->context) == NK_WIDGET_INVALID) return false;
    pressed = nk_button_behavior(&state, bounds, &ui->context->input, NK_BUTTON_DEFAULT);
    canvas = nk_window_get_canvas(ui->context);
    if ((state & NK_WIDGET_STATE_HOVER) != 0) {
        nk_fill_rect(canvas, bounds, 6.0f, ui->raised);
    }
    foreground = directory ? ui->accent : ui->muted;
    icon_bounds = nk_rect(bounds.x + 12.0f, bounds.y + 14.0f, 18.0f, 16.0f);
    if (directory) {
        const float points[] = {
            icon_bounds.x, icon_bounds.y + 3.0f,
            icon_bounds.x + 7.0f, icon_bounds.y + 3.0f,
            icon_bounds.x + 10.0f, icon_bounds.y + 6.0f,
            icon_bounds.x + icon_bounds.w, icon_bounds.y + 6.0f,
            icon_bounds.x + icon_bounds.w, icon_bounds.y + icon_bounds.h,
            icon_bounds.x, icon_bounds.y + icon_bounds.h,
            icon_bounds.x, icon_bounds.y + 3.0f
        };
        nk_stroke_polyline(canvas, points, 7, 1.5f, foreground);
    } else {
        nk_stroke_rect(canvas, icon_bounds, 2.0f, 1.5f, foreground);
        nk_stroke_line(canvas, icon_bounds.x + 5.0f, icon_bounds.y + 5.0f,
                       icon_bounds.x + 13.0f, icon_bounds.y + 5.0f, 1.5f, foreground);
        nk_stroke_line(canvas, icon_bounds.x + 5.0f, icon_bounds.y + 9.0f,
                       icon_bounds.x + 13.0f, icon_bounds.y + 9.0f, 1.5f, foreground);
    }
    name_bounds = nk_rect(bounds.x + 42.0f, bounds.y + 5.0f,
                          bounds.w * 0.62f - 42.0f, bounds.h - 10.0f);
    details_bounds = nk_rect(bounds.x + bounds.w * 0.62f, bounds.y + 5.0f,
                             bounds.w * 0.36f, bounds.h - 10.0f);
    nk_draw_text(canvas, name_bounds, name->data, (int)name->length,
                 ui->context->style.font, nk_rgba(0, 0, 0, 0), ui->text);
    nk_draw_text(canvas, details_bounds, details->data, (int)details->length,
                 ui->context->style.font, nk_rgba(0, 0, 0, 0), ui->muted);
    return pressed;
}

int32_t remolo_ui_edit(uint64_t handle, const fdn_string *name,
                       const fdn_string *value, uint64_t capacity,
                       fdn_string *result, bool *changed, bool *committed) {
    remolo_ui *ui = remolo_ui_from(handle);
    remolo_ui_edit_state *state;
    uint64_t length;
    nk_flags flags;
    struct nk_rect bounds;
    struct nk_window *window;
    if (ui == NULL || name == NULL || value == NULL || result == NULL ||
        changed == NULL || committed == NULL || name->length == 0 ||
        name->length >= REMOLO_UI_EDIT_NAME_CAPACITY ||
        (name->data == NULL && name->length != 0) ||
        (value->data == NULL && value->length != 0) ||
        memchr(name->data, '\0', name->length) != NULL ||
        memchr(value->data, '\0', value->length) != NULL ||
        capacity == 0 || capacity > INT32_MAX || value->length >= capacity) {
        return REMOLO_UI_INVALID;
    }
    state = remolo_ui_edit_for(ui, name, capacity);
    if (state == NULL) return REMOLO_UI_FAILED;
    length = SDL_strlen(state->buffer);
    if (length != value->length ||
        (length != 0 && SDL_memcmp(state->buffer, value->data, length) != 0)) {
        if (value->length != 0) {
            SDL_memcpy(state->buffer, value->data, value->length);
        }
        state->buffer[value->length] = '\0';
    }
    flags = (nk_flags)NK_EDIT_FIELD | (nk_flags)NK_EDIT_CLIPBOARD |
            (nk_flags)NK_EDIT_SIG_ENTER;
    bounds = nk_widget_bounds(ui->context);
    window = ui->context->current;
    if (nk_input_is_mouse_click_in_rect(&ui->context->input, NK_BUTTON_LEFT,
                                        bounds) ||
        nk_input_is_mouse_click_in_rect(&ui->context->input, NK_BUTTON_RIGHT,
                                        bounds)) {
        nk_edit_focus(ui->context, flags);
    }
    flags = nk_edit_string_zero_terminated(ui->context, flags, state->buffer,
                                           (int)state->capacity, nk_filter_default);
    remolo_ui_edit_menu(ui, window, bounds, state->buffer, state->capacity);
    fdn_string_drop(result);
    *result = foundation_runtime_string_copy(
        &(fdn_string){state->buffer, SDL_strlen(state->buffer), 0});
    *changed = (flags & NK_EDIT_COMMITED) != 0 ||
               result->length != value->length ||
               (result->length != 0 && SDL_memcmp(result->data, value->data, result->length) != 0);
    *committed = (flags & NK_EDIT_COMMITED) != 0;
    return REMOLO_UI_OK;
}

uint8_t *remolo_ui_image_buffer(uint64_t handle, uint64_t width, uint64_t height,
                                uint64_t *capacity) {
    remolo_ui *ui = remolo_ui_from(handle);
    SDL_Texture *texture;
    uint8_t *storage;
    uint64_t length;
    if (ui == NULL || capacity == NULL || width == 0 || height == 0 ||
        width > INT32_MAX || height > INT32_MAX || width > UINT64_MAX / height ||
        width * height > UINT64_MAX / 4) {
        return NULL;
    }
    length = width * height * 4;
    if (ui->remote_texture == NULL || ui->remote_width != width ||
        ui->remote_height != height) {
        texture = SDL_CreateTexture(ui->renderer, SDL_PIXELFORMAT_RGBA32,
                                    SDL_TEXTUREACCESS_STREAMING, (int)width, (int)height);
        if (texture == NULL) return NULL;
        if (!SDL_SetTextureScaleMode(texture, SDL_SCALEMODE_LINEAR)) {
            SDL_DestroyTexture(texture);
            return NULL;
        }
        storage = SDL_realloc(ui->remote_pixels, (size_t)length);
        if (storage == NULL) {
            SDL_DestroyTexture(texture);
            return NULL;
        }
        SDL_DestroyTexture(ui->remote_texture);
        ui->remote_texture = texture;
        ui->remote_pixels = storage;
        ui->remote_capacity = length;
        ui->remote_width = width;
        ui->remote_height = height;
    }
    *capacity = ui->remote_capacity;
    return ui->remote_pixels;
}

int32_t remolo_ui_image_commit(uint64_t handle) {
    remolo_ui *ui = remolo_ui_from(handle);
    if (ui == NULL || ui->remote_texture == NULL || ui->remote_pixels == NULL ||
        ui->remote_width > INT32_MAX / 4) {
        return REMOLO_UI_INVALID;
    }
    if (!SDL_UpdateTexture(ui->remote_texture, NULL, ui->remote_pixels,
                           (int)(ui->remote_width * 4))) {
        return REMOLO_UI_FAILED;
    }
    return REMOLO_UI_OK;
}

void remolo_ui_image(uint64_t handle) {
    remolo_ui *ui = remolo_ui_from(handle);
    struct nk_rect bounds;
    struct nk_rect target;
    struct nk_command_buffer *canvas;
    struct nk_image image;
    float source_ratio;
    float target_ratio;
    if (ui == NULL || ui->remote_texture == NULL) return;
    if (nk_widget(&bounds, ui->context) == NK_WIDGET_INVALID) return;
    target = bounds;
    source_ratio = (float)ui->remote_width / (float)ui->remote_height;
    target_ratio = bounds.w / bounds.h;
    if (target_ratio > source_ratio) {
        target.w = bounds.h * source_ratio;
        target.x += (bounds.w - target.w) * 0.5f;
    } else {
        target.h = bounds.w / source_ratio;
        target.y += (bounds.h - target.h) * 0.5f;
    }
    canvas = nk_window_get_canvas(ui->context);
    image = nk_image_ptr(ui->remote_texture);
    nk_draw_image(canvas, target, &image, nk_rgb(255, 255, 255));
    ui->remote_bounds = target;
    ui->remote_bounds_valid = true;
}

int32_t remolo_ui_poll_desktop_input(uint64_t handle, uint64_t *kind, uint64_t *x,
                                     uint64_t *y, uint64_t *button, uint64_t *key,
                                     bool *down, int64_t *delta) {
    remolo_ui *ui = remolo_ui_from(handle);
    remolo_ui_input input;
    if (ui == NULL || kind == NULL || x == NULL || y == NULL || button == NULL ||
        key == NULL || down == NULL || delta == NULL) {
        return -1;
    }
    if (ui->input_count == 0) return 0;
    input = ui->input_queue[ui->input_head];
    ui->input_head = (ui->input_head + 1) % REMOLO_UI_INPUT_CAPACITY;
    ui->input_count--;
    *kind = input.kind;
    *x = input.x;
    *y = input.y;
    *button = input.button;
    *key = input.key;
    *down = input.down;
    *delta = input.delta;
    return 1;
}

bool remolo_ui_desktop_controlled(uint64_t handle) {
    remolo_ui *ui = remolo_ui_from(handle);
    return ui != NULL && ui->remote_control;
}

void remolo_ui_release_desktop_control(uint64_t handle) {
    remolo_ui *ui = remolo_ui_from(handle);
    if (ui != NULL) remolo_ui_release_remote(ui);
}
