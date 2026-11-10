This envelope/public key was produced by the actual Go `server/cmd/release publish`
command on 2026-11-10 using a separate randomly generated test key. The private
key is deliberately absent. Tests reconstruct the fixture bytes explicitly:
base executable = 262144 A bytes + 262144 B bytes; target = A + C + B blocks;
the target Info.plist is the literal 111-byte string in the test. This preserves
a cross-language signature/domain/serialization fixture without binary blobs.
The test fixes time inside the signed validity interval; it never uses the
production trust key or connects to a production service.
