package config

// RBAC (spec section 31). Roles are coarse labels; permissions are what the
// server actually checks.

type Role = string

const (
	RoleUser        Role = "USER"
	RoleSupport     Role = "SUPPORT"
	RoleFinance     Role = "FINANCE"
	RoleRiskManager Role = "RISK_MANAGER"
	RoleAdmin       Role = "ADMIN"
	RoleSuperAdmin  Role = "SUPER_ADMIN"
)

var AllRoles = []Role{RoleUser, RoleSupport, RoleFinance, RoleRiskManager, RoleAdmin, RoleSuperAdmin}

var AdminRoles = []Role{RoleSupport, RoleFinance, RoleRiskManager, RoleAdmin, RoleSuperAdmin}

const (
	PermUsersRead    = "users:read"
	PermUsersWrite   = "users:write"
	PermUsersSuspend = "users:suspend"

	PermTradesRead   = "trades:read"
	PermTradesCancel = "trades:cancel"

	PermAssetsRead  = "assets:read"
	PermAssetsWrite = "assets:write"

	PermPayoutsRead  = "payouts:read"
	PermPayoutsWrite = "payouts:write"

	PermRiskRead  = "risk:read"
	PermRiskWrite = "risk:write"

	PermExposureRead = "exposure:read"

	PermHedgeRead  = "hedge:read"
	PermHedgeWrite = "hedge:write"

	PermWalletsRead   = "wallets:read"
	PermWalletsAdjust = "wallets:adjust"

	PermDepositsRead       = "deposits:read"
	PermDepositsApprove    = "deposits:approve"
	PermWithdrawalsRead    = "withdrawals:read"
	PermWithdrawalsApprove = "withdrawals:approve"

	PermMarketDataRead  = "market-data:read"
	PermMarketDataWrite = "market-data:write"

	PermReportsRead           = "reports:read"
	PermAuditRead             = "audit:read"
	PermReconciliationRead    = "reconciliation:read"
	PermReconciliationResolve = "reconciliation:resolve"

	PermSettingsRead  = "settings:read"
	PermSettingsWrite = "settings:write"

	PermAdminManage = "admin:manage"

	PermSupportRead  = "support:read"
	PermSupportWrite = "support:write"
)

// AllPermissions keeps the declaration order of the Node server.
var AllPermissions = []string{
	PermUsersRead, PermUsersWrite, PermUsersSuspend,
	PermTradesRead, PermTradesCancel,
	PermAssetsRead, PermAssetsWrite,
	PermPayoutsRead, PermPayoutsWrite,
	PermRiskRead, PermRiskWrite,
	PermExposureRead,
	PermHedgeRead, PermHedgeWrite,
	PermWalletsRead, PermWalletsAdjust,
	PermDepositsRead, PermDepositsApprove, PermWithdrawalsRead, PermWithdrawalsApprove,
	PermMarketDataRead, PermMarketDataWrite,
	PermReportsRead, PermAuditRead, PermReconciliationRead, PermReconciliationResolve,
	PermSettingsRead, PermSettingsWrite,
	PermAdminManage,
	PermSupportRead, PermSupportWrite,
}

var supportPermissions = []string{
	PermUsersRead, PermTradesRead, PermWalletsRead, PermDepositsRead, PermWithdrawalsRead,
	PermAssetsRead, PermMarketDataRead, PermSupportRead, PermSupportWrite,
}

var financePermissions = []string{
	PermUsersRead, PermWalletsRead, PermWalletsAdjust, PermDepositsRead, PermDepositsApprove,
	PermWithdrawalsRead, PermWithdrawalsApprove, PermTradesRead, PermReportsRead, PermReconciliationRead,
}

var riskManagerPermissions = []string{
	PermExposureRead, PermRiskRead, PermRiskWrite, PermHedgeRead, PermHedgeWrite,
	PermAssetsRead, PermPayoutsRead, PermTradesRead, PermMarketDataRead, PermReportsRead,
}

var adminPermissions = unique(
	supportPermissions,
	riskManagerPermissions,
	[]string{
		PermUsersWrite, PermUsersSuspend, PermTradesCancel, PermAssetsWrite, PermPayoutsWrite,
		PermMarketDataWrite, PermReportsRead, PermAuditRead, PermSettingsRead, PermReconciliationRead,
	},
)

var RolePermissions = map[Role][]string{
	RoleUser:        {},
	RoleSupport:     supportPermissions,
	RoleFinance:     financePermissions,
	RoleRiskManager: riskManagerPermissions,
	RoleAdmin:       adminPermissions,
	RoleSuperAdmin:  AllPermissions,
}

func unique(lists ...[]string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, list := range lists {
		for _, p := range list {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// PermissionsForRole returns the role's permissions plus any explicit extras.
func PermissionsForRole(role Role, extra []string) []string {
	return unique(RolePermissions[role], extra)
}

// HasPermission: the role's own set always counts, so a permission added to a
// role later reaches existing users without re-saving them.
func HasPermission(role Role, granted []string, required string) bool {
	if role == RoleSuperAdmin {
		return true
	}
	for _, p := range RolePermissions[role] {
		if p == required {
			return true
		}
	}
	for _, p := range granted {
		if p == required {
			return true
		}
	}
	return false
}

func IsAdminRole(role Role) bool {
	for _, r := range AdminRoles {
		if r == role {
			return true
		}
	}
	return false
}

func IsValidRole(role string) bool {
	for _, r := range AllRoles {
		if r == role {
			return true
		}
	}
	return false
}

func IsValidPermission(p string) bool {
	for _, x := range AllPermissions {
		if x == p {
			return true
		}
	}
	return false
}
