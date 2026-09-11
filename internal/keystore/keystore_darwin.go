//go:build darwin

package keystore

/*#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <Security/Security.h>
#include <CoreFoundation/CoreFoundation.h>
#include <string.h>
#include <stdlib.h>
static int save_item(const char*s,const char*a,const void*d,int n){
 SecKeychainItemRef old=NULL; OSStatus st=SecKeychainFindGenericPassword(NULL,(UInt32)strlen(s),s,(UInt32)strlen(a),a,NULL,NULL,&old);
 if(old){SecKeychainItemDelete(old); CFRelease(old);} st=SecKeychainAddGenericPassword(NULL,(UInt32)strlen(s),s,(UInt32)strlen(a),a,(UInt32)n,d,NULL); return (int)st;
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
	"time"
	"unsafe"
)

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
	s := C.CString(service)
	a := C.CString(Account(r.DBRoot, r.WxID))
	defer C.free(unsafe.Pointer(s))
	defer C.free(unsafe.Pointer(a))
	st := C.save_item(s, a, unsafe.Pointer(&b[0]), C.int(len(b)))
	if st != 0 {
		return fmt.Errorf("Keychain save failed: %d", st)
	}
	return nil
}
func Load(dbRoot, wxid string) (*Record, error) {
	s := C.CString(service)
	a := C.CString(Account(dbRoot, wxid))
	defer C.free(unsafe.Pointer(s))
	defer C.free(unsafe.Pointer(a))
	var data unsafe.Pointer
	var n C.UInt32
	st := C.load_item(s, a, &data, &n)
	if st != 0 {
		return nil, fmt.Errorf("Keychain item not found: %d", st)
	}
	defer C.free_item(data)
	b := C.GoBytes(data, C.int(n))
	var r Record
	if e := json.Unmarshal(b, &r); e != nil {
		return nil, fmt.Errorf("invalid keychain record: %w", e)
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
	st := C.delete_item(s, a)
	if st != 0 && st != C.errSecItemNotFound {
		return fmt.Errorf("Keychain delete failed: %d", st)
	}
	return nil
}
