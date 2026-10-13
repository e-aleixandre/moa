import Foundation

// Keychain access groups come from Info.plist so the team prefix is never
// hard-coded. A missing or unexpanded value is a build error surfaced at
// runtime as a Keychain failure: falling back to "no group" would let a new
// item land in whichever group the entitlements list first.
enum KeychainGroups {
    static var push: String? { resolved("MoaKeychainPushGroup") }
    static var deviceCredential: String? { resolved("MoaKeychainPrivateGroup") }

    private static func resolved(_ key: String) -> String? {
        guard
            let value = Bundle.main.object(forInfoDictionaryKey: key) as? String,
            !value.isEmpty,
            !value.contains("$("),
            !value.hasPrefix(".")
        else { return nil }
        return value
    }
}
