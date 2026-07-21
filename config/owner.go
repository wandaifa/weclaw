package config

// ownerUserIDs are the only WeChat identities with unrestricted access to the
// shared codex agent. Intentionally hardcoded rather than stored in
// config.json: owner status must not be changeable through the internal
// permissions API or a config file edit.
var ownerUserIDs = map[string]bool{
	"o9cq80ykt-8P0b7crARaJZtX1T9g@im.wechat": true, // personal WeChat
	"o9cq803depA5jxiybGv2PIr2ASjQ@im.wechat": true, // work WeChat
}

// IsOwner reports whether userID is the project owner.
func IsOwner(userID string) bool {
	return ownerUserIDs[userID]
}

// OwnerUserIDs returns the owner WeChat IDs, for display purposes only (e.g.
// labeling them "administrator" in the permissions admin UI). Not settable
// through any API.
func OwnerUserIDs() []string {
	ids := make([]string, 0, len(ownerUserIDs))
	for id := range ownerUserIDs {
		ids = append(ids, id)
	}
	return ids
}
