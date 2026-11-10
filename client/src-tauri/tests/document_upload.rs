use linknestx::transfer::{
    db::TransferDb,
    models::{Direction, Task, UploadTarget},
};

#[test]
fn document_destination_is_validated_and_survives_restart_without_a_chat_group() {
    assert!(UploadTarget::Document("../other".into())
        .validate()
        .is_err());
    assert!(UploadTarget::Group("".into()).validate().is_err());
    let root = tempfile::tempdir().unwrap();
    let db = TransferDb::open(root.path()).unwrap();
    let mut task = Task::new(
        "employee".into(),
        Direction::Upload,
        String::new(),
        "/test.pdf".into(),
    );
    task.document_id = Some("note-123".into());
    task.snapshot.document_id = task.document_id.clone();
    db.save(&task).unwrap();
    drop(db);
    let saved = TransferDb::open(root.path())
        .unwrap()
        .get(&task.snapshot.id, "employee")
        .unwrap();
    assert_eq!(saved.document_id.as_deref(), Some("note-123"));
    assert!(saved.group_id.is_none());
    assert_eq!(saved.snapshot.document_id, saved.document_id);
    let mut old = serde_json::to_value(task).unwrap();
    old.as_object_mut().unwrap().remove("document_id");
    old["snapshot"]
        .as_object_mut()
        .unwrap()
        .remove("documentId");
    assert!(serde_json::from_value::<Task>(old)
        .unwrap()
        .document_id
        .is_none());
}
