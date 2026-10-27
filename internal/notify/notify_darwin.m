#import <Foundation/Foundation.h>
#import <UserNotifications/UserNotifications.h>

#include "_cgo_export.h"

// MakoNotifyDelegate shows notifications while the app is in front too,
// and hands Go the target of the one the user clicks.
@interface MakoNotifyDelegate : NSObject <UNUserNotificationCenterDelegate>
@end

@implementation MakoNotifyDelegate
- (void)userNotificationCenter:(UNUserNotificationCenter *)center
       willPresentNotification:(UNNotification *)notification
         withCompletionHandler:(void (^)(UNNotificationPresentationOptions))done {
	done(UNNotificationPresentationOptionBanner | UNNotificationPresentationOptionList |
	     UNNotificationPresentationOptionSound);
}

- (void)userNotificationCenter:(UNUserNotificationCenter *)center
    didReceiveNotificationResponse:(UNNotificationResponse *)response
             withCompletionHandler:(void (^)(void))done {
	id target = response.notification.request.content.userInfo[@"target"];
	if ([response.actionIdentifier isEqualToString:UNNotificationDefaultActionIdentifier] &&
	    [target isKindOfClass:[NSString class]]) {
		makoNotifyClicked((char *)[target UTF8String]);
	}
	done();
}
@end

static MakoNotifyDelegate *delegate;

bool makoNotifyBundled(void) {
	return [[NSBundle mainBundle] bundleIdentifier] != nil;
}

void makoNotifySetup(void) {
	UNUserNotificationCenter *center = [UNUserNotificationCenter currentNotificationCenter];
	delegate = [MakoNotifyDelegate new];
	center.delegate = delegate;
	[center requestAuthorizationWithOptions:(UNAuthorizationOptionAlert | UNAuthorizationOptionSound)
	                      completionHandler:^(BOOL granted, NSError *err){
	                      }];
}

void makoNotifyPost(const char *title, const char *body, const char *target) {
	@autoreleasepool {
		NSString *t = [NSString stringWithUTF8String:target];
		UNMutableNotificationContent *content = [[[UNMutableNotificationContent alloc] init] autorelease];
		content.title = [NSString stringWithUTF8String:title];
		content.body = [NSString stringWithUTF8String:body];
		content.sound = [UNNotificationSound defaultSound];
		content.userInfo = @{@"target" : t};
		content.threadIdentifier = t;
		// One identifier for one target: the next replaces the last.
		UNNotificationRequest *request = [UNNotificationRequest requestWithIdentifier:t content:content trigger:nil];
		[[UNUserNotificationCenter currentNotificationCenter] addNotificationRequest:request
		                                                       withCompletionHandler:nil];
	}
}
