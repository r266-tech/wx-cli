//go:build darwin && cgo

package keystore

/*#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <Security/Security.h>
#include <CoreFoundation/CoreFoundation.h>
#include <string.h>
#include <stdlib.h>
static int save_item(const char*s,const char*a,const void*d,int n){
 SecKeychainItemRef old=NULL; OSStatus st=SecKeychainFindGenericPassword(NULL,(UInt32)strlen(s),s,(UInt32)strlen(a),a,NULL,NULL,&old);
 if(st==errSecSuccess){ st=SecKeychainItemModifyAttributesAndData(old,NULL,(UInt32)n,d); CFRelease(old); return (int)st; }
 if(st!=errSecItemNotFound) return (int)st;
 st=SecKeychainAddGenericPassword(NULL,(UInt32)strlen(s),s,(UInt32)strlen(a),a,(UInt32)n,d,NULL); return (int)st;
}
static int load_item(const char*s,const char*a,void**d,UInt32*n){
 return (int)SecKeychainFindGenericPassword(NULL,(UInt32)strlen(s),s,(UInt32)strlen(a),a,n,d,NULL);
}
static void free_item(void*d){ if(d) SecKeychainItemFreeContent(NULL,d); }
static int delete_item(const char*s,const char*a){
 SecKeychainItemRef old=NULL; OSStatus st=SecKeychainFindGenericPassword(NULL,(UInt32)strlen(s),s,(UInt32)strlen(a),a,NULL,NULL,&old); if(st!=errSecSuccess) return (int)st; st=SecKeychainItemDelete(old); CFRelease(old); return (int)st;
}
*/
import "C"
import (
	"encoding/json"
	"fmt"
	"sync"
	"time"
	"unsafe"
)

// The legacy Keychain API controls interaction at process scope. Serialize all
// operations and restore the original setting even if a request fails.
var keychainAccess sync.Mutex

func Available() bool { return true }

func withInteraction(allowed bool, fn func() C.int) error {
	keychainAccess.Lock()
	defer keychainAccess.Unlock()
	var previous C.Boolean
	if st := C.SecKeychainGetUserInteractionAllowed(&previous); st != 0 {
		return keychainError(C.int(st))
	}
	var requested C.Boolean
	if allowed {
		requested = 1
	}
	if st := C.SecKeychainSetUserInteractionAllowed(requested); st != 0 {
		return keychainError(C.int(st))
	}
	defer C.SecKeychainSetUserInteractionAllowed(previous)
	return keychainError(fn())
}

func keychainError(status C.int) error {
	switch status {
	case 0:
		return nil
	case C.errSecInteractionNotAllowed:
		return ErrInteractionRequired
	case C.errSecItemNotFound:
		return ErrItemNotFound
	case C.errSecUserCanceled, C.errSecAuthFailed:
		return ErrAuthorizationDenied
	default:
		return fmt.Errorf("Keychain operation failed (OSStatus %d)", status)
	}
}

func Save(r Record) error {
	if r.SchemaVersion == 0 {
		r.SchemaVersion = 1
	}
	if r.UpdatedAt == "" {
		r.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	b, e := json.Marshal(r)
	if e != nil {
		return e
	}
	defer clear(b)
	s := C.CString(service)
	a := C.CString(Account(r.DBRoot, r.WxID))
	defer C.free(unsafe.Pointer(s))
	defer C.free(unsafe.Pointer(a))
	return withInteraction(false, func() C.int {
		return C.save_item(s, a, unsafe.Pointer(&b[0]), C.int(len(b)))
	})
}
func Load(dbRoot, wxid string) (*Record, error) {
	return load(dbRoot, wxid, false)
}

// LoadWithAuthorization permits the OS to ask the user for access to this
// account's existing item. It does not alter the item or its access list itself.
func LoadWithAuthorization(dbRoot, wxid string) (*Record, error) {
	return load(dbRoot, wxid, true)
}

func load(dbRoot, wxid string, interactive bool) (*Record, error) {
	s := C.CString(service)
	a := C.CString(Account(dbRoot, wxid))
	defer C.free(unsafe.Pointer(s))
	defer C.free(unsafe.Pointer(a))
	var data unsafe.Pointer
	var n C.UInt32
	err := withInteraction(interactive, func() C.int { return C.load_item(s, a, &data, &n) })
	if err != nil {
		return nil, err
	}
	defer C.free_item(data)
	b := C.GoBytes(data, C.int(n))
	defer clear(b)
	var r Record
	if e := json.Unmarshal(b, &r); e != nil {
		return nil, fmt.Errorf("invalid keychain record")
	}
	if r.WxID != wxid || r.DBRoot != dbRoot {
		return nil, fmt.Errorf("keychain account mismatch")
	}
	return &r, nil
}
func Delete(dbRoot, wxid string) error {
	s := C.CString(service)
	a := C.CString(Account(dbRoot, wxid))
	defer C.free(unsafe.Pointer(s))
	defer C.free(unsafe.Pointer(a))
	err := withInteraction(false, func() C.int { return C.delete_item(s, a) })
	if err == ErrItemNotFound {
		return nil
	}
	return err
}
