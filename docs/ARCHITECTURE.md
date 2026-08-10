# Grok-Nobody Architecture

```
Vue UI  --HTTP-->  Go API (:17890)
                     |- engine.RegisterEngine -> pkg/grokregister.RunBatch
                     |- engine.EduService     -> pkg/cfemail.Provision + StorePushedOTP
                     |- plugins.Center       -> local process / docker
                     '- store.JSONStore      -> settings.json
```

Packages derived from the Sub2API project (import paths rewritten):

- internal/pkg/grokregister
- internal/pkg/cfemail
- internal/pkg/captcha
- internal/pkg/errors
- internal/pkg/xai (+ util helpers)
