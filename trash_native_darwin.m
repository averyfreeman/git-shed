//go:build darwin && cgo
#import <Foundation/Foundation.h>
#include <stdlib.h>
#include <string.h>
char *shed_trash(const char *path, char **error) {
 @autoreleasepool {
  NSURL *source = [NSURL fileURLWithPath:[NSString stringWithUTF8String:path]];
  NSURL *result = nil;
  NSError *failure = nil;
  BOOL ok = [[NSFileManager defaultManager] trashItemAtURL:source resultingItemURL:&result error:&failure];
  if (!ok) { *error = strdup([[failure localizedDescription] UTF8String]); return NULL; }
  return strdup([[result path] UTF8String]);
 }
}
