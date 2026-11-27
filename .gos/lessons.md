<!-- gos:generated lessons-md render-hash:bd3fd9d9f16d0cf1 — DO NOT EDIT: regenerated from lessons.json on every mutation; hand edits are overwritten by design. -->

# Lessons (cross-task experience library)

## [project] repo knowledge · never expires

- (b4d26988) companion client dual-reader (Call reader + events pump on one conn) deadlocked recovery: EOF drop cascade starved post-restart dial; single-reader pump with pending-map routing is the fix — one reader per conn, never two
- (8badf27a) windows daemon must be launched session-detached (WMI Win32_Process.Create proven on winbuild): a process started inside an ssh session dies with the session, which masqueraded as a transport bug for an hour

<!-- taxonomy: [project] = repo/domain knowledge, never expires; [model] = model-behavior correction, expires (90d default). ids are entry address prefixes: gos lessons rm/amend <id-prefix>. -->
