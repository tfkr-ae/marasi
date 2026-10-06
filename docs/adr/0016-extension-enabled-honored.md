# HTTP modifiers honor Extension.Enabled

Enabled is stored on every extension row but HTTP `processRequest` / `processResponse` ignored it. Only some websocket paths checked it. The control API now exposes enable and disable, so a client that sets `enabled` false must see compass stop filtering and checkpoint stop intercepting. Persist-only would leave the flag as a lie. Unloading the runtime would make compass look missing and return `ErrExtensionNotFound`. Disabled means skip that extension's pipeline hook. The runtime stays loaded so update, call, logs, and settings still work.

**Considered options:** persist the flag and leave HTTP modifiers unchanged; unload the runtime on disable; treat disabled compass as not found. Rejected because the flag would not change traffic, and missing compass is an error rather than an off switch.
