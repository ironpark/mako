// Thin C layer between the Go host and the tree-sitter runtime.
//
// Every function here takes and returns plain i32 values so the Go side
// never has to marshal C structs by value. Pointers are offsets into the
// module's linear memory, which grammar modules share.

#include <assert.h>
#include <ctype.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <wchar.h>
#include <wctype.h>

#include "tree_sitter/api.h"

#define EXPORT(name) __attribute__((export_name(#name), used))

// Address of every libc function a grammar may import, in the order of
// libc-symbols.txt (build.sh generates libc_fns.h from it). Taking the
// addresses keeps them linked and puts them in the shared function table,
// so GOT.func imports can be answered with these values.
static void *const libc_fns[] = {
#include "libc_fns.h"
};

EXPORT(shim_libc_fns) void *const *shim_libc_fns(void) { return libc_fns; }
EXPORT(shim_libc_count) uint32_t shim_libc_count(void) {
  return sizeof(libc_fns) / sizeof(libc_fns[0]);
}

// Zeroed, aligned block for a grammar's static data (its __memory_base).
EXPORT(shim_alloc_zeroed) void *shim_alloc_zeroed(uint32_t align, uint32_t size) {
  if (align < 16) align = 16;
  size_t n = (size + align - 1) / align * align;
  if (n == 0) n = align;
  void *p = aligned_alloc(align, n);
  if (p) memset(p, 0, n);
  return p;
}

EXPORT(shim_malloc) void *shim_malloc(uint32_t n) { return malloc(n); }
EXPORT(shim_free) void shim_free(void *p) { free(p); }

EXPORT(shim_language_abi) uint32_t shim_language_abi(const TSLanguage *l) {
  return ts_language_abi_version(l);
}

// A parser bound to one language, or NULL if its ABI is unsupported.
EXPORT(shim_parser_new) TSParser *shim_parser_new(const TSLanguage *l) {
  TSParser *p = ts_parser_new();
  if (!ts_parser_set_language(p, l)) {
    ts_parser_delete(p);
    return NULL;
  }
  return p;
}

EXPORT(shim_parser_delete) void shim_parser_delete(TSParser *p) { ts_parser_delete(p); }

EXPORT(shim_parse) TSTree *shim_parse(TSParser *p, const TSTree *old,
                                      const char *src, uint32_t len) {
  TSTree *t = ts_parser_parse_string(p, old, src, len);
  if (!t) ts_parser_reset(p);
  return t;
}

EXPORT(shim_tree_delete) void shim_tree_delete(TSTree *t) { ts_tree_delete(t); }

EXPORT(shim_tree_edit) void shim_tree_edit(TSTree *t, uint32_t start_byte,
                                           uint32_t old_end_byte, uint32_t new_end_byte,
                                           uint32_t start_row, uint32_t start_col,
                                           uint32_t old_end_row, uint32_t old_end_col,
                                           uint32_t new_end_row, uint32_t new_end_col) {
  TSInputEdit e = {
    .start_byte = start_byte,
    .old_end_byte = old_end_byte,
    .new_end_byte = new_end_byte,
    .start_point = {start_row, start_col},
    .old_end_point = {old_end_row, old_end_col},
    .new_end_point = {new_end_row, new_end_col},
  };
  ts_tree_edit(t, &e);
}

// S-expression of the root node; the caller frees it with shim_free.
EXPORT(shim_tree_sexpr) char *shim_tree_sexpr(const TSTree *t) {
  return ts_node_string(ts_tree_root_node(t));
}

// out[0] = error offset, out[1] = TSQueryError.
EXPORT(shim_query_new) TSQuery *shim_query_new(const TSLanguage *l, const char *src,
                                               uint32_t len, uint32_t *out) {
  TSQueryError err = TSQueryErrorNone;
  TSQuery *q = ts_query_new(l, src, len, &out[0], &err);
  out[1] = (uint32_t)err;
  return q;
}

EXPORT(shim_query_delete) void shim_query_delete(TSQuery *q) { ts_query_delete(q); }
EXPORT(shim_query_pattern_count) uint32_t shim_query_pattern_count(const TSQuery *q) {
  return ts_query_pattern_count(q);
}
EXPORT(shim_query_capture_count) uint32_t shim_query_capture_count(const TSQuery *q) {
  return ts_query_capture_count(q);
}
EXPORT(shim_query_string_count) uint32_t shim_query_string_count(const TSQuery *q) {
  return ts_query_string_count(q);
}
EXPORT(shim_query_capture_name) const char *shim_query_capture_name(const TSQuery *q, uint32_t id,
                                                                    uint32_t *len) {
  return ts_query_capture_name_for_id(q, id, len);
}
EXPORT(shim_query_string_value) const char *shim_query_string_value(const TSQuery *q, uint32_t id,
                                                                    uint32_t *len) {
  return ts_query_string_value_for_id(q, id, len);
}
EXPORT(shim_query_predicates) const TSQueryPredicateStep *shim_query_predicates(
    const TSQuery *q, uint32_t pattern, uint32_t *len) {
  return ts_query_predicates_for_pattern(q, pattern, len);
}

EXPORT(shim_query_pattern_start) uint32_t shim_query_pattern_start(const TSQuery *q,
                                                                  uint32_t pattern) {
  return ts_query_start_byte_for_pattern(q, pattern);
}

static TSQueryCursor *cursor;
static uint32_t *buf;
static uint32_t buf_len, buf_cap;

static void push(uint32_t v) {
  if (buf_len == buf_cap) {
    buf_cap = buf_cap ? buf_cap * 2 : 4096;
    buf = realloc(buf, buf_cap * sizeof(uint32_t));
  }
  buf[buf_len++] = v;
}

// Runs q over the bytes [start, end) of t and returns every match as a
// flat record list: pattern, capture count, then (capture id, start byte,
// end byte) per capture. The buffer is reused by the next call.
EXPORT(shim_query_matches) const uint32_t *shim_query_matches(const TSQuery *q, const TSTree *t,
                                                              uint32_t start, uint32_t end,
                                                              uint32_t match_limit,
                                                              uint32_t *out_len) {
  if (!cursor) cursor = ts_query_cursor_new();
  ts_query_cursor_set_match_limit(cursor, match_limit);
  ts_query_cursor_set_byte_range(cursor, start, end);
  ts_query_cursor_exec(cursor, q, ts_tree_root_node(t));
  buf_len = 0;
  TSQueryMatch m;
  while (ts_query_cursor_next_match(cursor, &m)) {
    push(m.pattern_index);
    push(m.capture_count);
    for (uint16_t i = 0; i < m.capture_count; i++) {
      const TSQueryCapture *c = &m.captures[i];
      push(c->index);
      push(ts_node_start_byte(c->node));
      push(ts_node_end_byte(c->node));
    }
  }
  *out_len = buf_len;
  return buf;
}
