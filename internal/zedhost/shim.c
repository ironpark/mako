#include "shim.h"
#include "_cgo_export.h"

wasm_engine_t *sb_engine_new(void) {
  wasm_config_t *cfg = wasm_config_new();
  wasmtime_config_wasm_component_model_set(cfg, true);
  return wasm_engine_new_with_config(cfg);
}

static wasmtime_error_t *sb_trampoline(void *data, wasmtime_context_t *ctx,
                                       const wasmtime_component_func_type_t *ty,
                                       wasmtime_component_val_t *args,
                                       size_t nargs,
                                       wasmtime_component_val_t *results,
                                       size_t nresults) {
  (void)ty;
  return sbHostCall((uintptr_t)data, ctx, args, nargs, results, nresults);
}

static void sb_finalize(void *data) { sbHostFree((uintptr_t)data); }

wasmtime_error_t *sb_add_func(wasmtime_component_linker_instance_t *li,
                              const char *name, size_t len, uintptr_t handle) {
  return wasmtime_component_linker_instance_add_func(
      li, name, len, sb_trampoline, (void *)handle, sb_finalize);
}

// Host resources are handed to the guest as borrows only, so there is
// nothing to release when one is dropped.
static wasmtime_error_t *sb_drop(void *data, wasmtime_context_t *ctx,
                                 uint32_t rep) {
  (void)data;
  (void)ctx;
  (void)rep;
  return NULL;
}

wasmtime_error_t *sb_add_resource(wasmtime_component_linker_instance_t *li,
                                  const char *name, size_t len, uint32_t ty) {
  wasmtime_component_resource_type_t *rt =
      wasmtime_component_resource_type_new_host(ty);
  wasmtime_error_t *err = wasmtime_component_linker_instance_add_resource(
      li, name, len, rt, sb_drop, NULL, NULL);
  wasmtime_component_resource_type_delete(rt);
  return err;
}

wasmtime_error_t *sb_set_wasi(wasmtime_context_t *ctx, const char *dir,
                              size_t envc, const char **names,
                              const char **values) {
  wasi_config_t *wasi = wasi_config_new();
  if (!wasi_config_set_env(wasi, envc, names, values)) {
    wasi_config_delete(wasi);
    return wasmtime_error_new("wasi: bad environment");
  }
  if (!wasi_config_preopen_dir(wasi, dir, ".", true)) {
    wasi_config_delete(wasi);
    return wasmtime_error_new("wasi: cannot open the extension's work directory");
  }
  return wasmtime_context_set_wasi(ctx, wasi);
}

wasmtime_component_val_t *sb_val_box(void) {
  wasmtime_component_val_t v;
  v.kind = WASMTIME_COMPONENT_BOOL;
  v.of.boolean = false;
  return wasmtime_component_val_new(&v);
}
