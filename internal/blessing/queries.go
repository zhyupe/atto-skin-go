package blessing

import (
	_ "embed"
	"strings"
)

//go:embed sql/columns.sql
var columnsSQL string

//go:embed sql/accounts.sql
var accountsSQL string

//go:embed sql/players.sql
var playersSQL string

//go:embed sql/textures.sql
var texturesSQL string

//go:embed sql/uuids.sql
var uuidsSQL string

//go:embed sql/algorithm.sql
var algorithmSQL string

func sourceQuery(query, prefix, playerName, uuidPID string) string {
	return strings.NewReplacer("{{prefix}}", prefix, "{{player_name}}", playerName, "{{uuid_pid}}", uuidPID).
		Replace(query)
}
