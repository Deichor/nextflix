package com.nextflix.tv.feature.subtitles

import com.nextflix.tv.core.i18n.AppLanguage
import com.nextflix.tv.core.prefs.SubtitleChoice
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class SubtitleRulesTest {
    private fun sub(id: String, lang: String, source: String = "subdl", synced: Boolean = false, forced: Boolean = false) =
        SubtitleTrack(id = id, source = source, language = lang, synced = synced, forced = forced)

    @Test
    fun normalizesLanguageCodes() {
        assertEquals("tr", Languages.norm("tur"))
        assertEquals("tr", Languages.norm("TR"))
        assertEquals("tr", Languages.norm("tr-TR"))
        assertEquals("pt", Languages.norm("pt_BR"))
        assertEquals("zh", Languages.norm("chi"))
        assertEquals("abc", Languages.norm("abc"))
        assertEquals("", Languages.norm("und"))
        assertEquals("", Languages.norm(null))
        assertEquals("", Languages.norm("  "))
    }

    @Test
    fun namesLanguagesInTheUiLanguage() {
        assertEquals("Turkish", Languages.name(AppLanguage.En, "tr"))
        assertEquals("Türkçe", Languages.name(AppLanguage.Tr, "tr"))
        assertEquals("İngilizce", Languages.name(AppLanguage.Tr, "en"))
        assertEquals("", Languages.name(AppLanguage.En, ""))
        assertEquals("Qqq", Languages.name(AppLanguage.En, "qqq"))
    }

    @Test
    fun defaultPick() {
        val items = listOf(sub("en1", "en"), sub("tr1", "tur"), sub("tr2", "tr", synced = true), sub("trf", "tr", forced = true, synced = true))
        // Turkish profile, English audio: Turkish, synced first, never forced while a regular one exists
        assertEquals("tr2", SubtitleRules.pick(items, AppLanguage.Tr, null, "eng"))
        // ... Turkish audio: none
        assertNull(SubtitleRules.pick(items, AppLanguage.Tr, null, "tur"))
        // English profile without a choice: none
        assertNull(SubtitleRules.pick(items, AppLanguage.En, null, "en"))
        // The last choice wins: off, the exact id, else that language
        assertNull(SubtitleRules.pick(items, AppLanguage.Tr, SubtitleChoice.Off, "en"))
        assertEquals("tr1", SubtitleRules.pick(items, AppLanguage.Tr, SubtitleChoice.Language("tr", "tr1"), "en"))
        assertEquals("en1", SubtitleRules.pick(items, AppLanguage.Tr, SubtitleChoice.Language("en", "gone"), "en"))
        assertNull(SubtitleRules.pick(items, AppLanguage.En, SubtitleChoice.Language("de"), "en"))
        // Only forced ones: the forced one
        assertEquals("f", SubtitleRules.pick(listOf(sub("f", "tr", forced = true)), AppLanguage.Tr, null, "en"))
    }

    @Test
    fun aiTranslationDoesNotCountAsTheLanguage() {
        val items = listOf(sub("ai", "tr", source = "ai"), sub("en1", "en"))
        assertFalse(SubtitleRules.hasLang(items, "tr"))
        assertTrue(SubtitleRules.hasLang(items, "en"))
    }

    @Test
    fun mergeKeepsTheNewerAnswerFirst() {
        val merged = SubtitleRules.merge(listOf(sub("a", "en"), sub("b", "en")), listOf(sub("b", "en", synced = true), sub("c", "tr")))
        assertEquals(listOf("b", "c", "a"), merged.map { it.id })
        assertTrue(merged.first().synced)
    }

    @Test
    fun lateSubtitlesArriveAndPickUnlessTheViewerChose() {
        val shown = listOf(sub("en1", "en"))
        val fresh = listOf(sub("tr1", "tr"), sub("tr2", "tr", synced = true))
        val late = SubtitleRules.late(shown, fresh, "tr", viewerChose = false)
        assertTrue(late.arrived)
        assertEquals("tr2", late.pick)
        assertEquals(listOf("tr1", "tr2", "en1"), late.items.map { it.id })
        assertNull(SubtitleRules.late(shown, fresh, "tr", viewerChose = true).pick)
        // Already there: not new
        assertFalse(SubtitleRules.late(fresh, fresh, "tr", viewerChose = false).arrived)
        // An AI item in the shown list does not stop a real one from arriving
        assertTrue(SubtitleRules.late(listOf(sub("ai", "tr", source = "ai")), fresh, "tr", false).arrived)
    }

    @Test
    fun fallbackPrefersAnotherProvider() {
        val items = listOf(
            sub("os1", "tr", source = "opensubtitles"),
            sub("os2", "tr", source = "opensubtitles", synced = true),
            sub("sd1", "tr", source = "subdl"),
            sub("sdf", "tr", source = "subdl", forced = true),
            sub("en1", "en", source = "subdl"),
        )
        assertEquals("sd1", SubtitleRules.fallback(items, "os1", setOf("os1")))
        assertEquals("os2", SubtitleRules.fallback(items, "os1", setOf("os1", "sd1")))
        assertNull(SubtitleRules.fallback(items, "os1", setOf("os1", "sd1", "os2")))
        assertNull(SubtitleRules.fallback(items, "missing", emptySet()))
    }

    @Test
    fun oneMenuRowPerLanguageProfileLanguagesFirst() {
        val items = listOf(
            sub("de1", "ger"), sub("en1", "en"), sub("fr1", "fr"), sub("tr1", "tr"), sub("en2", "eng"), sub("tr2", "tur"),
        )
        val tr = SubtitleRules.perLanguage(items, listOf("tr", "en"), AppLanguage.Tr)
        assertEquals(listOf("tr1", "en1", "de1", "fr1"), tr.map { it.id })
        val en = SubtitleRules.perLanguage(items, listOf("en", "tr"), AppLanguage.En)
        assertEquals(listOf("en1", "tr1", "fr1", "de1"), en.map { it.id })
    }

    @Test
    fun cueAnswers() {
        assertEquals(SubtitleRules.CueAnswer.Show, SubtitleRules.cueAnswer(12, false, 0))
        assertEquals(SubtitleRules.CueAnswer.Show, SubtitleRules.cueAnswer(12, true, 3))
        assertEquals(SubtitleRules.CueAnswer.Show, SubtitleRules.cueAnswer(0, true, 0))
        assertEquals(SubtitleRules.CueAnswer.Fallback, SubtitleRules.cueAnswer(0, false, 0))
        assertEquals(SubtitleRules.CueAnswer.Retry, SubtitleRules.cueAnswer(0, false, 1))
        assertEquals(SubtitleRules.CueAnswer.Retry, SubtitleRules.cueAnswer(0, false, 7))
    }

    @Test
    fun retryWaitDoublesUpToFiveMinutes() {
        assertEquals(listOf(5000L, 10000L, 20000L, 40000L), (1..4).map(SubtitleRules::retryMs))
        assertEquals(300000L, SubtitleRules.retryMs(7))
        assertEquals(300000L, SubtitleRules.retryMs(30))
    }

    @Test
    fun onlyACompleteAnswerWithFinalTimingIsFinal() {
        for (sync in listOf("synced", "unchanged", "replaced", "poor", "original")) {
            assertTrue(sync, SubtitleRules.finalCues(false, sync))
            assertFalse(sync, SubtitleRules.finalCues(true, sync))
        }
        // Provisional timing without the partial mark (no live audio for 90 s), or an unknown state
        for (sync in listOf("live", "pending", "failed", "-", "")) assertFalse(sync, SubtitleRules.finalCues(false, sync))
    }

    @Test
    fun partialAnswersArePolledEvery15sProvisionalOnesEveryMinuteFinalOnesNever() {
        assertEquals(SubtitleRepository.LIVE_CUES_POLL_MS, SubtitleRules.pollMs(true, "live"))
        assertEquals(SubtitleRepository.LIVE_CUES_POLL_MS, SubtitleRules.pollMs(true, "original"))
        assertEquals(SubtitleRepository.AI_CUES_POLL_MS, SubtitleRules.pollMs(true, "pending", translating = true))
        assertEquals(SubtitleRepository.PROVISIONAL_CUES_POLL_MS, SubtitleRules.pollMs(false, "live"))
        assertEquals(SubtitleRepository.PROVISIONAL_CUES_POLL_MS, SubtitleRules.pollMs(false, "pending"))
        assertEquals(SubtitleRepository.PROVISIONAL_CUES_POLL_MS, SubtitleRules.pollMs(false, "failed"))
        assertNull(SubtitleRules.pollMs(false, "synced"))
        assertNull(SubtitleRules.pollMs(false, "original"))
        assertEquals(listOf(15_000L, 3_000L, 60_000L), listOf(SubtitleRepository.LIVE_CUES_POLL_MS, SubtitleRepository.AI_CUES_POLL_MS, SubtitleRepository.PROVISIONAL_CUES_POLL_MS))
    }

    @Test
    fun untimedPartialAnswersArePolledEvery5sInTheFirst3Minutes() {
        val fast = SubtitleRepository.PENDING_FAST_FOR_MS
        assertEquals(SubtitleRepository.PENDING_CUES_POLL_MS, SubtitleRules.pollMs(true, "pending", shownMs = 0))
        assertEquals(SubtitleRepository.PENDING_CUES_POLL_MS, SubtitleRules.pollMs(true, "pending", shownMs = fast - 1))
        assertEquals(SubtitleRepository.LIVE_CUES_POLL_MS, SubtitleRules.pollMs(true, "pending", shownMs = fast))
        assertEquals(SubtitleRepository.LIVE_CUES_POLL_MS, SubtitleRules.pollMs(true, "pending"))
        assertEquals(SubtitleRepository.LIVE_CUES_POLL_MS, SubtitleRules.pollMs(true, "live", shownMs = 0))
        assertEquals(SubtitleRepository.AI_CUES_POLL_MS, SubtitleRules.pollMs(true, "pending", translating = true, shownMs = 0))
        assertEquals(SubtitleRepository.PROVISIONAL_CUES_POLL_MS, SubtitleRules.pollMs(false, "pending", shownMs = 0))
        assertEquals(listOf(5_000L, 180_000L), listOf(SubtitleRepository.PENDING_CUES_POLL_MS, fast))
    }

    @Test
    fun delayIsLimitedAndRounded() {
        assertEquals(1.5, SubtitleRules.clampDelay(1.5), 0.0)
        assertEquals(0.3, SubtitleRules.clampDelay(0.30000000004), 0.0)
        assertEquals(60.0, SubtitleRules.clampDelay(75.0), 0.0)
        assertEquals(-60.0, SubtitleRules.clampDelay(-61.0), 0.0)
        assertEquals(0.0, SubtitleRules.clampDelay(Double.NaN), 0.0)
        assertEquals("+1.5", SubtitleRules.delayText(1.5, AppLanguage.En))
        assertEquals("−0,5", SubtitleRules.delayText(-0.5, AppLanguage.Tr))
        assertEquals("0.0", SubtitleRules.delayText(0.0, AppLanguage.En))
    }
}
