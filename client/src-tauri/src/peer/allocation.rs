use super::*;

#[derive(Clone, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct Allocation {
    pub start_index: usize,
    pub end_index: usize,
    pub expires_at: String,
}
impl Allocation {
    pub fn prioritize(&self, pending: &mut [Chunk], concurrency: usize) -> usize {
        pending.sort_by_key(|chunk| !self.contains(chunk.index));
        pending
            .iter()
            .take_while(|chunk| self.contains(chunk.index))
            .count()
            .min(concurrency)
    }
    pub fn valid(&self, chunks: usize) -> bool {
        self.start_index <= self.end_index
            && self.end_index <= chunks
            && (chunks == 0 || self.start_index < self.end_index)
            && chrono::DateTime::parse_from_rfc3339(&self.expires_at).is_ok_and(|expiry| {
                expiry > chrono::Utc::now()
                    && expiry <= chrono::Utc::now() + chrono::Duration::seconds(65)
            })
    }
    pub fn contains(&self, index: usize) -> bool {
        index >= self.start_index && index < self.end_index
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn allocation_bounds_and_first_batch_preserve_minimum_three_chunk_partition() {
        for count in [3, 12] {
            let mut claimed = std::collections::HashSet::new();
            for region in 0..3 {
                let allocation = Allocation {
                    start_index: region * count / 3,
                    end_index: (region + 1) * count / 3,
                    expires_at: (chrono::Utc::now() + chrono::Duration::seconds(60)).to_rfc3339(),
                };
                assert!(allocation.valid(count));
                let mut pending: Vec<_> = (0..count)
                    .map(|index| Chunk {
                        index,
                        hash: "hash".into(),
                        size: 1,
                    })
                    .collect();
                let batch = allocation.prioritize(&mut pending, 3);
                assert_eq!(batch, (count / 3).min(3));
                for chunk in &pending[..batch] {
                    assert!(claimed.insert(chunk.index));
                }
            }
        }
        let invalid = Allocation {
            start_index: 0,
            end_index: 4,
            expires_at: (chrono::Utc::now() + chrono::Duration::seconds(60)).to_rfc3339(),
        };
        assert!(!invalid.valid(3));
        let expired = Allocation {
            start_index: 0,
            end_index: 1,
            expires_at: "2000-01-01T00:00:00Z".into(),
        };
        assert!(!expired.valid(3));
    }
}
impl PeerManager {
    pub async fn allocate(&self, manifest: &Manifest, task_id: &str) -> Option<Allocation> {
        if !self.policy.read().await.enabled {
            return None;
        }
        let value = tokio::time::timeout(
            Duration::from_secs(2),
            self.auth.json(
                "POST",
                "/api/v1/p2p/allocate",
                Some(json!({"versionId":manifest.version_id,"taskId":task_id})),
            ),
        )
        .await
        .ok()?
        .ok()?;
        let allocation: Allocation = serde_json::from_value(value).ok()?;
        allocation
            .valid(manifest.chunks.len())
            .then_some(allocation)
    }
    pub async fn wait_for_partial_sources(self: &Arc<Self>, manifest: &Manifest) {
        let _ = tokio::time::timeout(Duration::from_secs(1), async {
            loop {
                self.discoveries.lock().await.remove(&manifest.version_id);
                if self
                    .sources(manifest)
                    .await
                    .iter()
                    .any(|source| manifest.chunks.iter().any(|chunk| source.has_chunk(chunk)))
                {
                    break;
                }
                tokio::time::sleep(Duration::from_millis(100)).await;
            }
        })
        .await;
    }
}
