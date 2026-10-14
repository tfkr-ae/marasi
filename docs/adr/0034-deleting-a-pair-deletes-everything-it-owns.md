# Deleting a pair deletes everything that belongs to it

Deleting a request/response pair removes its note, its launchpad, finding, test case and Armory links, its WebSocket connection and messages, its proxy logs, its search index rows, its highlight and its sitemap counts. Marasi does not refuse to delete a pair that is evidence for a finding; the client warns before it asks. Proxy logs go with the pair rather than surviving unlinked, because a log about a pair that no longer exists has nothing left to explain. A pair still in flight or held by Checkpoint can be deleted; its late response is then dropped without error.

**Consequences:** the existing `ON DELETE CASCADE` foreign keys do most of this, so every child table needs an index that leads with `request_id`, or each deleted pair scans the child tables. The contentless search index has no foreign key, so delete removes its rows explicitly.
