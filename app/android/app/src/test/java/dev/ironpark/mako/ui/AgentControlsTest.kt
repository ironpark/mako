package dev.ironpark.mako.ui

import dev.ironpark.mako.v2.AgentModel
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class AgentControlsTest {

    private fun catalogModel(
        id: String,
        name: String = id,
        efforts: List<String> = emptyList(),
        overflow: Boolean = false,
    ): AgentModel = AgentModel.newBuilder()
        .setId(id)
        .setName(name)
        .addAllEfforts(efforts)
        .setOverflow(overflow)
        .build()

    @Test
    fun shortModelFormatsClaudeIdsAndAliases() {
        assertEquals("Opus 4.6", shortModel("claude-opus-4-6"))
        assertEquals("Opus 4.6", shortModel("claude-opus-4-6[1m]"))
        assertEquals("Opus 5.5", shortModel("claude-opus-5-5"))
        assertEquals("Sonnet 4", shortModel("claude-sonnet-4-20250514"))
        assertEquals("Sonnet 4.5", shortModel("claude-sonnet-4-5-20250929"))
        assertEquals("Fable 5.1", shortModel("claude-fable-5-1[1m]"))
        assertEquals("Haiku 4.5", shortModel("claude-haiku-4-5-20251001"))
        assertEquals("Sonnet 3.5", shortModel("claude-3-5-sonnet-20241022"))
        assertEquals("Opus 3", shortModel("claude-3-opus-20240229"))
        assertEquals("Opus 4.6 Fast", shortModel("claude-opus-4-6-fast"))
        assertEquals("Opus", shortModel("opus"))
        assertEquals("Sonnet", shortModel("sonnet"))
        assertEquals("Haiku", shortModel("haiku"))
        assertEquals("Fable", shortModel("fable"))
    }

    @Test
    fun shortModelFormatsGptCodexAndOSeries() {
        assertEquals("GPT-5.4", shortModel("gpt-5.4"))
        assertEquals("GPT-6.2 Sol", shortModel("gpt-6.2-sol"))
        assertEquals("GPT-6 Luna", shortModel("gpt-6-luna"))
        assertEquals("GPT-5", shortModel("gpt-5"))
        assertEquals("GPT-5", shortModel("gpt-5-2025-08-07"))
        assertEquals("GPT-5 Mini", shortModel("gpt-5-mini"))
        assertEquals("GPT-5.3 Codex", shortModel("gpt-5.3-codex"))
        assertEquals("GPT-4o Mini", shortModel("gpt-4o-mini-2024-07-18"))
        assertEquals("GPT-OSS 120B", shortModel("gpt-oss-120b"))
        assertEquals("o3", shortModel("o3"))
        assertEquals("o3", shortModel("o3-2025-04-16"))
        assertEquals("o4-mini", shortModel("o4-mini"))
        assertEquals("o4-mini", shortModel("o4-mini-2025-04-16"))
        assertEquals("o3-pro", shortModel("o3-pro"))
    }

    @Test
    fun shortModelFormatsGeminiAndLeavesUnknownUnmangled() {
        assertEquals("Gemini 2.5 Pro", shortModel("gemini-2.5-pro"))
        assertEquals("Gemini 4 Neon", shortModel("gemini-4-neon"))
        assertEquals("Gemini 2.5 Flash", shortModel("gemini-2.5-flash-20250514"))
        assertEquals("Gemini 2.5 Pro", shortModel("gemini-2.5-pro[preview]"))
        assertEquals("Gemini 2.5 Flash Lite", shortModel("gemini-2-5-flash-lite"))

        assertEquals("deepseek-r1", shortModel("deepseek-r1"))
        assertEquals("qwen2.5-coder:32b", shortModel("qwen2.5-coder:32b"))
        assertEquals("custom/my-model", shortModel("custom/my-model"))
        assertEquals("custom-model", shortModel("custom-model-20250514"))
        assertEquals("custom-model", shortModel("custom-model[1m]"))
        assertEquals("", shortModel(""))
    }

    @Test
    fun matchesModelHandlesExactSuffixesAliasesAndPreventsSubstringFalsePositives() {
        // Exact and case-insensitive matches.
        assertTrue(matchesModel("gpt-5.4", "gpt-5.4"))
        assertTrue(matchesModel("GPT-5.4", "gpt-5.4"))

        // Date and bracket tag stripping.
        assertTrue(matchesModel("claude-opus-4-6[1m]", "claude-opus-4-6"))
        assertTrue(matchesModel("claude-sonnet-4-20250514", "claude-sonnet-4"))
        assertTrue(matchesModel("gpt-5-2025-08-07", "gpt-5"))
        assertTrue(matchesModel("o4-mini-2025-04-16", "o4-mini"))

        // Claude alias matches.
        assertTrue(matchesModel("claude-opus-4-6", "opus"))
        assertTrue(matchesModel("claude-3-5-sonnet-20241022", "sonnet"))
        assertTrue(matchesModel("claude-haiku-4-5-20251001", "haiku"))
        assertTrue(matchesModel("opus", "claude-opus-4-6"))
        assertFalse(matchesModel("claude-opus-4-6", "sonnet"))
        assertFalse(matchesModel("my-opus-custom", "opus"))

        // Agy variant effort suffix match to base model.
        assertTrue(matchesModel("gemini-4-neon-high", "gemini-4-neon"))
        assertFalse(matchesModel("gemini-4-neon-low", "gemini-4-neon-high"))

        // No substring false positives!
        assertFalse(matchesModel("gpt-5-mini", "gpt-5"))
        assertFalse(matchesModel("gpt-5", "gpt-5-mini"))
        assertFalse(matchesModel("gpt-5.4-codex", "gpt-5.4"))
        assertFalse(matchesModel("o4-mini", "o4"))
        assertFalse(matchesModel("gemini-2.5-pro-exp", "gemini-2.5-pro"))
        assertFalse(matchesModel("", "gpt-5"))
        assertFalse(matchesModel("gpt-5", ""))
    }

    @Test
    fun effortsForModelFiltersPerCatalogAndHidesFixedEffortModels() {
        val catalog = listOf(
            catalogModel("gemini-4-neon", "Gemini 4 Neon", efforts = listOf("low", "medium", "high")),
            catalogModel("orchestrator", "Orchestrator", efforts = emptyList()),
            catalogModel("gpt-oss-120b-medium", "GPT-OSS 120B (Medium)", efforts = emptyList()),
            catalogModel("claude-opus-4-6", "Claude Opus 4.6", efforts = listOf("low", "medium", "high", "max")),
            catalogModel("legacy-model", "Legacy", efforts = listOf("xhigh"), overflow = true),
        )
        val fallback = listOf("low", "medium", "high", "xhigh", "max")

        // Empty catalog falls back cleanly.
        assertEquals(fallback, effortsForModel("claude-opus-4-6", emptyList(), fallback))

        // Active variable-effort model exposes ONLY its own efforts.
        assertEquals(listOf("low", "medium", "high"), effortsForModel("gemini-4-neon", catalog, fallback))
        assertEquals(listOf("low", "medium", "high"), effortsForModel("gemini-4-neon-high", catalog, fallback))
        assertEquals(listOf("low", "medium", "high", "max"), effortsForModel("claude-opus-4-6[1m]", catalog, fallback))
        assertEquals(listOf("low", "medium", "high", "max"), effortsForModel("opus", catalog, fallback))

        // Fixed-effort models return empty even when fallback efforts exist.
        assertEquals(emptyList<String>(), effortsForModel("orchestrator", catalog, fallback))
        assertEquals(emptyList<String>(), effortsForModel("gpt-oss-120b-medium", catalog, fallback))

        // Unselected model ("Default") uses union of catalog efforts (main before overflow) when no fallback given.
        assertEquals(listOf("low", "medium", "high", "max", "xhigh"), effortsForModel("", catalog))

        // Unmatched model with fallback falls back to controls efforts.
        assertEquals(fallback, effortsForModel("unknown-model", catalog, fallback))
        assertNull(findCatalogModel(catalog, "unknown-model"))
    }

    @Test
    fun modelDisplayNamePrefersCatalogNameOrFormatsId() {
        assertEquals("Gemini 4 Neon", modelDisplayName(catalogModel("gemini-4-neon", "Gemini 4 Neon")))
        assertEquals("GPT-5.4", modelDisplayName(catalogModel("gpt-5.4", "gpt-5.4")))
        assertEquals("Opus 4.6", modelDisplayName(catalogModel("claude-opus-4-6", "")))
    }

    @Test
    fun resolveWorktreeBranchKeepsExplicitOrGeneratesValidBranchName() {
        assertEquals("feat/login", resolveWorktreeBranch("  feat/login  ", "claude", "ignored", 0L))

        val fromAgent = resolveWorktreeBranch("", "claude", "", 1_700_000_000_000L)
        assertTrue("Expected mako/claude- prefix, got $fromAgent", fromAgent.startsWith("mako/claude-"))
        assertFalse(fromAgent.startsWith("-"))
        assertFalse(fromAgent.contains(".."))
        assertFalse(fromAgent.contains(" "))

        val fromSession = resolveWorktreeBranch("   ", "codex", "Fix Login / Auth!!", 1_700_000_000_000L)
        assertTrue("Expected mako/fix-login-auth- prefix, got $fromSession", fromSession.startsWith("mako/fix-login-auth-"))

        val fromSymbols = resolveWorktreeBranch("", "---", "!!!", 1_700_000_000_000L)
        assertTrue("Expected mako/session- fallback, got $fromSymbols", fromSymbols.startsWith("mako/session-"))
    }
}
