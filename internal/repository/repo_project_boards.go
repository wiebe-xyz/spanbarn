package repository

import "database/sql"

// deleteProjectBoards removes everything boards keep for a project: panels,
// boards, release markers and saved queries, in the order the foreign keys need.
func deleteProjectBoards(tx *sql.Tx, projectID int64) error {
	for _, stmt := range []string{
		`DELETE FROM board_panels WHERE board_id IN (SELECT id FROM boards WHERE project_id = ?)`,
		`DELETE FROM boards WHERE project_id = ?`,
		`DELETE FROM releases WHERE project_id = ?`,
		`DELETE FROM saved_queries WHERE project_id = ?`,
	} {
		if _, err := tx.Exec(stmt, projectID); err != nil {
			return err
		}
	}
	return nil
}
