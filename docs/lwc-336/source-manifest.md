# LWC-336 pre-implementation source manifest

Captured before any LWC-336 product-code edits in this worktree. The immutable Git base is `26527e79376e57756842b210e5a57eaa48526d9f`; that commit SHA identifies the base only, not the dirty 375/377 candidate. This manifest fingerprints the complete 19-file dirty candidate at the start of LWC-336. The 375 contract/spec and its reports are included as dependency inputs in this full working-tree snapshot.

Each digest is SHA-256 of the exact file bytes. `manifest_sha256` is SHA-256 of the UTF-8 bytes of the following path-sorted lines, each formatted `repo-relative-path sha256` and terminated with `\n`.

```text
apps/bff/cmd/bff/main.go e927a1b50802f139ff6d3694774eaba1c7faa6acce4de93997612630463dc69f
apps/bff/cmd/bff/project_keys_test.go f41b81c322a479dea1cf747d167cc937c06d1d48699ddaf24354e53ba7d66516
apps/bff/internal/auth/jwt.go 4871a30896f790dd7e95efa36371b165da6b3edfe19f914fa3cf74fa8d381f2a
apps/bff/internal/auth/project_key.go 59a10e4a13d098dfa5db963c50c474ac5a313685c113227c815b9be71c4f8acb
apps/bff/internal/auth/project_key_test.go eb0209ab45f7b22e09a657424bc73a8f55d727fe8ffe2e5c3e902abd46a2e143
apps/bff/internal/handler/v1/handler.go 5449d6343d97c557139900e32d985936afcdd82e566b3d66f29df7e772d4a626
apps/bff/internal/handler/v1/project_keys.go 0593c603fcc6fe998343efd797619cdfad6b8ff6a4265df686e2a6aa87d53810
apps/bff/internal/handler/v1/project_keys_test.go b2b7313b8725818c7b64e6ebf9c651e608366ba72aefa8eba2d48300454b07a9
apps/frontend/src/components/AccountSettingsModal.tsx a3b2f1752e081a6304947feb110fceb5851f6dbc6cb0edc21cff4a2d6513482e
apps/frontend/src/components/ProjectKeysSection.tsx c135f46b3294d069bfc4ce3c996eb8b8fd9202fb1ca5eead01b742b3ef3d720c
apps/frontend/src/lib/project-keys.ts cb0589f9419ae454e39b7d9c3296b8a7328ffe0fc71dacaec64e939cb0f789de
apps/frontend/src/messages/en.json f361c29ac91534ae1c4d5408661f9a239140373ca0cf79d093ba861287fe63dc
apps/frontend/src/messages/zh-TW.json a81005f1c95590fc11726285d893ed8367f1f8acf9e9521c6ebced04770f4252
apps/frontend/tests/lwc-346-account-settings.test.tsx 4fb7d76a1c1a4ed13be0bd482c1adfb5e9bc8f84750b50da290d2e6c9f401021
apps/frontend/tests/lwc-377-project-keys.test.tsx e090718e51b6801f3ad3ba1ca4f8b565d609efdad63023ea6ba7091e5b78671b
docs/lwc-375/errata.md 175f7be200b744ae9ef8d7d6cf271aa3a5216f4745c3c7c50db3acc65bca29dd
docs/lwc-375/report.md 9aa844f89b55702f03455b8f31fedb416f29deebf3574d0ac0f3b13fcec4e193
docs/lwc-375/spec.md 30e44560a951786503b6a42d58d20f496dd6eda4d4a2036408759a76862a86d8
docs/lwc-377/report.md a300ce59b7292130ad238996c8bb5908e13763025b218ce2013fb79073dd03b1
```

Pre-336 candidate manifest SHA-256: `5cf4f58953c88e6cddc76a70421e9ae1cf65f1ac867368d7154eb2b8208c9364`.

The LWC-336 delta is separate and will be listed in `docs/lwc-336/report.md` with its own post-change paths and hashes. No 375/377 content is rewritten to manufacture a new base commit or candidate identity.
