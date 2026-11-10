use linknestx::peer::protocol::{decode, encode, Assembly, Frame, Grant, MAX_FRAME};
use linknestx::transfer::models::{hash, Chunk};

#[test]
fn frame_and_assembly_reject_oversized_unrequested_reordered_and_duplicate_bytes() {
    let chunk = Chunk {
        index: 2,
        hash: hash(b"abcdef"),
        size: 6,
    };
    let mut assembly = Assembly::new("request".into(), "version".into(), chunk.clone()).unwrap();
    let data = Frame::Data {
        request_id: "request".into(),
        version_id: "version".into(),
        index: 2,
        hash: chunk.hash.clone(),
        offset: 0,
        end: false,
    };
    let encoded = encode(&data, b"abc").unwrap();
    let (decoded, bytes) = decode(&encoded).unwrap();
    assert!(assembly.push(&decoded, &bytes).unwrap().is_none());
    assert!(assembly.push(&decoded, &bytes).is_err());
    assert!(encode(&data, &vec![0; MAX_FRAME]).is_err());
    assert!(decode(&vec![0; MAX_FRAME + 1]).is_err());
    let mut wrong = Assembly::new("other".into(), "version".into(), chunk.clone()).unwrap();
    assert!(wrong.push(&decoded, &bytes).is_err());
    let end = Frame::Data {
        request_id: "request".into(),
        version_id: "version".into(),
        index: 2,
        hash: chunk.hash,
        offset: 3,
        end: true,
    };
    assert_eq!(assembly.push(&end, b"def").unwrap().unwrap(), b"abcdef");
    assert!(assembly.push(&end, b"def").is_err());
}

#[test]
fn grant_must_match_trusted_signaling_recipient_and_exact_manifest_chunk() {
    let chunk = Chunk {
        index: 0,
        hash: hash(b"abc"),
        size: 3,
    };
    let grant = Grant {
        user_id: "user".into(),
        device_id: "device".into(),
        session_id: "session".into(),
        version_id: "version".into(),
        chunk_hash: chunk.hash.clone(),
        size: 3,
        expires_at: "2099-01-01T00:00:00Z".into(),
        signature: "signature".into(),
    };
    assert!(grant.bind("user", "device", "version", &chunk).is_ok());
    assert!(grant.bind("attacker", "device", "version", &chunk).is_err());
    assert!(grant.bind("user", "attacker", "version", &chunk).is_err());
    assert!(grant
        .bind("user", "device", "other-version", &chunk)
        .is_err());
}
