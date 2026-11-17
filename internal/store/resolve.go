package store

import (
	"fmt"
	"path/filepath"
)

// ResolveDecision records the human's answer to a require_approval decision, matched by
// the tool call id that produced it.
func ResolveDecision(home, sessionID, callID string, approved bool, note string) error {
	if callID == "" {
		return fmt.Errorf("store: resolve needs a call id")
	}
	home = Home(home)
	if err := ensureHome(home); err != nil {
		return err
	}
	db, err := openDB(filepath.Join(home, "blackbox.db"))
	if err != nil {
		return err
	}
	defer db.Close()

	id := ExternalID("oc-", sessionID)
	result, err := db.Exec(
		`UPDATE policy_decisions
		    SET approved = ?, reason = reason || ?
		  WHERE id = (
		        SELECT id FROM policy_decisions
		         WHERE session_id = ? AND call_id = ?
		         ORDER BY id DESC LIMIT 1)`,
		boolInt(approved), note, id, callID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("store: no decision for call %q", callID)
	}
	return nil
}
