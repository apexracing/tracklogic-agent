package security

import (
	"fmt"
	"sync"
)

type Permission string

const (
	PermReadFile  Permission = "read_file"
	PermWriteFile Permission = "write_file"
	PermExec      Permission = "execute_command"
	PermNetAccess Permission = "network_access"
	PermReadDB    Permission = "read_database"
	PermWriteDB   Permission = "write_database"
	PermSendEmail Permission = "send_email"
)

// PermissionManager controls tool permissions and can be replaced by callers
// when permissions come from an external policy system.
type PermissionManager interface {
	Allow(...Permission)
	Deny(...Permission)
	Check(Permission) error
	IsAllowed(Permission) bool
	SetRole(Role)
}

// ListPermissionManager is the default in-memory PermissionManager.
type ListPermissionManager struct {
	mu        sync.RWMutex
	allowList map[Permission]bool
	denyList  map[Permission]bool
}

func NewPermissionManager() *ListPermissionManager {
	return &ListPermissionManager{
		allowList: make(map[Permission]bool),
		denyList:  make(map[Permission]bool),
	}
}

func (pm *ListPermissionManager) Allow(perms ...Permission) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for _, p := range perms {
		pm.allowList[p] = true
		delete(pm.denyList, p)
	}
}

func (pm *ListPermissionManager) Deny(perms ...Permission) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for _, p := range perms {
		pm.denyList[p] = true
		delete(pm.allowList, p)
	}
}

func (pm *ListPermissionManager) Check(perm Permission) error {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	if pm.denyList[perm] {
		return fmt.Errorf("permission %q is explicitly denied", perm)
	}
	if pm.allowList[perm] {
		return nil
	}
	return fmt.Errorf("permission %q is not granted", perm)
}

func (pm *ListPermissionManager) IsAllowed(perm Permission) bool {
	return pm.Check(perm) == nil
}

type Role string

const (
	RoleAdmin Role = "admin"
	RoleUser  Role = "user"
	RoleGuest Role = "guest"
)

var roleDefaults = map[Role][]Permission{
	RoleAdmin: {PermReadFile, PermWriteFile, PermExec, PermNetAccess, PermReadDB, PermWriteDB, PermSendEmail},
	RoleUser:  {PermReadFile, PermWriteFile, PermNetAccess, PermReadDB},
	RoleGuest: {PermReadFile},
}

func (pm *ListPermissionManager) SetRole(role Role) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.allowList = make(map[Permission]bool)
	pm.denyList = make(map[Permission]bool)
	if perms, ok := roleDefaults[role]; ok {
		for _, p := range perms {
			pm.allowList[p] = true
		}
	}
}

// RequiredPermission returns the permission needed to invoke a tool, if any.
func RequiredPermission(toolName string) (Permission, bool) {
	switch toolName {
	case "read_file", "list_dir":
		return PermReadFile, true
	case "write_file":
		return PermWriteFile, true
	case "http_get":
		return PermNetAccess, true
	default:
		if len(toolName) > 4 && toolName[:4] == "mcp_" {
			return PermNetAccess, true
		}
		return "", false
	}
}
