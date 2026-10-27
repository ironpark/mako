// The few helpers the Go side needs around wasmtime's component C API:
// trampolines for host functions and resources, which call back into Go,
// and engine and store setup.
#ifndef MAKO_ZEDHOST_SHIM_H
#define MAKO_ZEDHOST_SHIM_H

#include <stdint.h>
#include <wasmtime.h>

wasm_engine_t *sb_engine_new(void);

wasmtime_error_t *sb_add_func(wasmtime_component_linker_instance_t *li,
                              const char *name, size_t len, uintptr_t handle);

wasmtime_error_t *sb_add_resource(wasmtime_component_linker_instance_t *li,
                                  const char *name, size_t len, uint32_t ty);

wasmtime_error_t *sb_set_wasi(wasmtime_context_t *ctx, const char *dir,
                              size_t envc, const char **names,
                              const char **values);

// sb_val_box allocates a value, a false bool, for an option, result or
// variant to point at.
wasmtime_component_val_t *sb_val_box(void);

#endif
