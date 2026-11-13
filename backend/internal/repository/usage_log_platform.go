package repository

import "fmt"

// Query the immutable snapshot only. Current account/group platforms cannot
// establish historical identity, including for soft-deleted entities.
func appendUsageLogPlatformCondition(conditions []string, args []any, platform, prefix string) ([]string, []any) {
	if platform == "" {
		return conditions, args
	}
	column := prefix + "platform"
	if platform == "unknown" {
		return append(conditions, "("+column+" IS NULL OR "+column+" = '')"), args
	}
	return append(conditions, fmt.Sprintf("%s = $%d", column, len(args)+1)), append(args, platform)
}
