# LWC-417 engineering disposition r1

Status: supplemental implementation note. The frozen r2 contract and Brief consumer contract remain unchanged; this note records the accepted implementation/deployment boundary in live LWC-417 comment 4-3136.

## Accepted scope

The Auth/CaC implementation supports explicit Daily Brief policy in both `dev` and `prod`, using the existing per-environment Auth projection, snapshot, loader, and verifier inputs. The fixed resource remains `https://brief.rayer.idv.tw/mcp` with the single `brief:manage` scope. The production verifier audience, when a production Brief policy is explicitly configured, is exactly `https://llm-wiki-auth-a5nkmux6pq-de.a.run.app`; the development verifier audience remains exactly `https://llm-wiki-auth-dev-a5nkmux6pq-de.a.run.app`. Audience validation is environment-specific and rejects cross-environment values and aliases.

## Deployment boundary

The current canonical production Auth policy remains disabled with an empty verifier caller service account. This source compatibility repair does not activate or publish a production Brief policy and does not change production services. A production caller identity must be supplied through the existing CaC field before any separately authorized activation. The DEV-only deployment authority and selected-component release boundary remain as recorded in the accepted disposition; no cloud release action is part of this source repair.

The production service URL above is used only as a nonsecret synthetic verifier audience in local compatibility tests. Those tests prove that an explicitly configured production policy survives generation, input snapshot validation, config identity calculation, materialization, and Auth runtime loading; they do not prove cloud identity, production activation, or deployed readback.
