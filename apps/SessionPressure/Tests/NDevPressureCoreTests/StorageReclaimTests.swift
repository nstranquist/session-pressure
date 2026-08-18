import Foundation
import Testing
@testable import NDevPressureCore

@Suite("Storage reclaim mapping")
struct StorageReclaimTests {
    @Test("providers fixture decodes tiers, factory-only, and blocked reasons")
    func decodeProvidersFixture() throws {
        let env = try decode(StorageProvidersEnvelope.self, fixture: "storage-providers")
        #expect(env.action == "storage.providers")
        #expect(env.storage?.level == .critical)
        #expect(env.storagePolicy?.enforceAdmission == false)

        let profiles = try #require(env.providers.first(where: { $0.id == "browser-dead-profiles" }))
        #expect(profiles.tier == .autoSafe)
        #expect(profiles.isFactoryOnly)
        #expect(profiles.isActionable == false)
        #expect(profiles.blockedReason?.contains("pageskein") == true)

        let pnpm = try #require(env.providers.first(where: { $0.id == "pnpm-store" }))
        #expect(pnpm.tier == .autoSafe)
        #expect(pnpm.isActionable == false)
        #expect(pnpm.blockedReason == "a pnpm process is active")

        let trash = try #require(env.providers.first(where: { $0.id == "user-trash" }))
        #expect(trash.tier == .operatorProvider)
        #expect(trash.isActionable)

        let goCache = try #require(env.providers.first(where: { $0.id == "go-build-cache" }))
        #expect(goCache.isActionable == false)

        let modules = try #require(env.providers.first(where: { $0.id == "go-module-cache" }))
        #expect(modules.tier == .reportOnly)
        #expect(modules.isActionable == false)
    }

    @Test("preview fixture maps receipt lines from typed apply JSON")
    func receiptLinesFromPreviewFixture() throws {
        let env = try decode(StorageApplyEnvelope.self, fixture: "storage-apply-preview")
        #expect(env.apply == false)
        #expect(env.autoSafe == true)
        let command = StorageReclaim.applyArguments(autoSafe: true, provider: nil, apply: false)
        #expect(command == ["--json", "session", "pressure", "storage", "apply", "--auto-safe"])
        #expect(!command.contains("--apply"))
        #expect(!command.joined(separator: " ").contains("grok"))
        #expect(!command.joined(separator: " ").contains("codex"))

        let lines = StorageReclaim.receiptLines(from: env, command: command)
        let texts = lines.map(\.text)
        #expect(texts.contains(where: { $0.contains("--auto-safe") }))
        #expect(texts.contains(where: { $0.contains("apply=false") }))
        #expect(texts.contains(where: { $0.contains("factory-only") && $0.contains("browser-dead-profiles") }))
        #expect(texts.contains(where: { $0.contains("pnpm-store") && $0.contains("a pnpm process is active") }))
        #expect(!texts.contains(where: { $0.contains("go-build-cache") && $0.contains("actionable") }))
    }

    @Test("receipt fixture keeps factory-blocked skip and does not invent agent argv")
    func receiptLinesFromApplyFixture() throws {
        let env = try decode(StorageApplyEnvelope.self, fixture: "storage-apply-receipt")
        #expect(env.apply == true)
        let command = StorageReclaim.applyArguments(autoSafe: true, provider: nil, apply: true)
        #expect(command.contains("--apply"))
        let lines = StorageReclaim.receiptLines(from: env, command: command)
        let texts = lines.map(\.text)
        #expect(texts.contains(where: { $0.contains("skipped browser-dead-profiles") && $0.contains("pageskein") }))
        #expect(texts.contains(where: { $0.contains("receipt pnpm-store") && $0.contains("completed") }))
        #expect(!command.contains(where: { $0.contains(";") || $0.contains("rm") }))
    }

    @Test("named provider argv stays closed and rejects shell text")
    func namedProviderArgvIsClosed() {
        let args = StorageReclaim.applyArguments(autoSafe: false, provider: "user-trash", apply: true)
        #expect(args == ["--json", "session", "pressure", "storage", "apply", "--provider", "user-trash", "--apply"])
        let forced = StorageReclaim.applyArguments(autoSafe: false, provider: "user-trash", apply: true, force: true)
        #expect(forced == ["--json", "session", "pressure", "storage", "apply", "--provider", "user-trash", "--force", "--apply"])
        let autoSafeForced = StorageReclaim.applyArguments(autoSafe: true, provider: nil, apply: true, force: true)
        #expect(!autoSafeForced.contains("--force"))
        let rejected = StorageReclaim.applyArguments(autoSafe: false, provider: "user-trash; rm -rf", apply: true)
        #expect(!rejected.contains("user-trash; rm -rf"))
        #expect(StorageProviderID.isSafe("user-trash; rm -rf") == false)
        #expect(StorageTargetFree.isSafe("30GiB; rm") == false)
        #expect(StorageTargetFree.isSafe("30GiB"))
    }

    private func decode<T: Decodable>(_ type: T.Type, fixture: String) throws -> T {
        let url = try #require(
            Bundle.module.url(forResource: fixture, withExtension: "json", subdirectory: "Fixtures")
                ?? Bundle.module.url(forResource: fixture, withExtension: "json"),
            "\(fixture).json fixture is missing from the test bundle"
        )
        return try PressureJSON.decode(type, from: Data(contentsOf: url))
    }
}
