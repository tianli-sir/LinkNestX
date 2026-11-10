# LinkNestX 0.2 UI verification

`fixture-*.png` are screenshots of the real production React components with deterministic local fixture data. They are not evidence of a live server, real file transfer, real clipboard, or installed update.

The capture script is `client/tests/visual/capture-v2.mjs`; the preview entry is `client/preview-v2.html`. Production builds include only the normal app entry. Captures cover desktop login/chat/draft/profile/direct chat/contacts/transfer center/settings and 390 px mobile conversation/chat layouts. `fixture-visual-result.json` records browser errors and overflow measurements.

The Task 3 report is `.superpowers/sdd/2026-11-10-collaboration-v2-plan/task-3-report.md`. Any parent-generated real-backend evidence is separately labeled in this folder.

`real-browser-integration.json` and `real-*.png` use the built Web application
served by the real Go server, with temporary local accounts and storage. The
browser uses an ordinary HTTP test hostname explicitly mapped to loopback, so
secure-context-only APIs are unavailable as on the intended LAN HTTP deployment.
The nine checks cover actual installer download routes plus numeric login, cookies, live messages, exact visible read
receipts, modal/inactive suppression, friend approval and direct chat, actual
download byte integrity, capability/CSRF denial and session restoration. No
production host is contacted. `client/tests/integration-v2.mjs` reproduces it.

`native-go-smoke.json` records the separate actual native engine / Go integration
run: a 48 MiB file downloaded from two peers and the server, including announcing
and serving already verified parts before completion, service restart/resume,
revoked-device denial, and 205-record native CSV/JSON export. The 20 GB check
validates manifest geometry; it does not claim a physical 20 GB transfer.

`real-mobile-review.json` and `real-group-management-mobile.png` verify the
management entry and return navigation at 390 px against the built Web/Go app.
These are distinct from the fixture screenshots and are regenerated after
relevant fixes.

Review follow-up: `real-group-management-mobile.png` and `real-mobile-review.json` use the **built production SPA with an isolated real local Go server**. They verify that at 390 px, group management opens before selecting any conversation and returns to the list, with no browser errors or horizontal overflow. The scope is separate from the parent's eight-scenario `real-browser-integration.json`.


Latest member-profile follow-up: the real mobile screenshot now shows a member's
name, avatar and numeric account, resolved through the permitted profile API.
The run verifies that the avatar opens/closes the profile, no internal UUID is
shown and the browser makes zero `/api/v1/users` requests. All four checks pass,
with no browser errors and `scrollWidth=clientWidth=390`. Denied/disabled profiles,
late responses after membership removal, friend actions, rollback retry,
repeated-download progress and native paste hints are additionally covered by
the targeted 47-test frontend run recorded in the Task 3 report. OS clipboard
behavior still requires the separate Windows/macOS manual checks.

`actual-macos-release.json` records the real 0.2.0 macOS application payload.
Both the offline publisher and the production Rust consumer verified it; the
consumer reconstructed the entire app over loopback and the resulting bundle
passed the system resource-signature check. The large 0.1→0.2 code change only
reuses404bytes; the separate deliberate delta fixture demonstrates substantial
reuse. Neither result claims that an old0.1 client can install automatically.
