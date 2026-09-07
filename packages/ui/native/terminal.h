#ifndef REMOLO_UI_TERMINAL_H
#define REMOLO_UI_TERMINAL_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

enum {
  REMOLO_TERMINAL_DEFAULT_COLOR = 256,
};

enum remolo_terminal_attribute {
  REMOLO_TERMINAL_BOLD = 1,
  REMOLO_TERMINAL_DIM = 2,
  REMOLO_TERMINAL_UNDERLINE = 4,
  REMOLO_TERMINAL_REVERSE = 8,
};

typedef struct remolo_terminal_cell {
  uint32_t rune;
  uint16_t foreground;
  uint16_t background;
  uint8_t attributes;
} remolo_terminal_cell;

typedef struct remolo_terminal remolo_terminal;

remolo_terminal *remolo_terminal_create(uint16_t columns, uint16_t rows);
void remolo_terminal_destroy(remolo_terminal *terminal);
bool remolo_terminal_resize(remolo_terminal *terminal, uint16_t columns,
                            uint16_t rows);
void remolo_terminal_write(remolo_terminal *terminal, const uint8_t *source,
                           size_t length);

const remolo_terminal_cell *
remolo_terminal_cells(const remolo_terminal *terminal);
const remolo_terminal_cell *
remolo_terminal_view_row(const remolo_terminal *terminal, uint16_t row,
                         size_t scroll_offset);
size_t remolo_terminal_history_rows(const remolo_terminal *terminal);
uint16_t remolo_terminal_columns(const remolo_terminal *terminal);
uint16_t remolo_terminal_rows(const remolo_terminal *terminal);
uint16_t remolo_terminal_cursor_column(const remolo_terminal *terminal);
uint16_t remolo_terminal_cursor_row(const remolo_terminal *terminal);
bool remolo_terminal_cursor_visible(const remolo_terminal *terminal);

#endif
