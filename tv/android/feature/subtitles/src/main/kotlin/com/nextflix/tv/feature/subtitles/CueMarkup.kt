package com.nextflix.tv.feature.subtitles

/**
 * Cue text -> styled runs (webOS player/SubtitleOverlay.tsx renderCue). Only <i>, <b> and <u> are kept (with
 * classes like <i.yellow> read as <i>), every other tag is dropped, entities are decoded and line breaks kept.
 * Nothing else is read as markup.
 */
internal object CueMarkup {
    data class Run(val text: String, val italic: Boolean = false, val bold: Boolean = false, val underline: Boolean = false)

    private val PARTS = Regex("<[^>]*>|\n")
    private val STYLE_TAG = Regex("""^<(/?)([ibu])(?:[\s.][^>]*)?>$""", RegexOption.IGNORE_CASE)
    private val ENTITY = Regex("&(#\\d+|#x[0-9a-f]+|[a-z]+);", RegexOption.IGNORE_CASE)
    private val ENTITIES = mapOf(
        "amp" to "&", "lt" to "<", "gt" to ">", "quot" to "\"", "apos" to "'", "nbsp" to " ", "lrm" to "", "rlm" to "",
    )

    fun decode(s: String): String {
        if ('&' !in s) return s
        return ENTITY.replace(s) { m ->
            val e = m.groupValues[1]
            if (e.startsWith("#")) {
                val code = if (e.length > 1 && (e[1] == 'x' || e[1] == 'X')) e.substring(2).toIntOrNull(16) else e.substring(1).toIntOrNull()
                if (code != null && Character.isValidCodePoint(code)) String(Character.toChars(code)) else m.value
            } else {
                ENTITIES[e.lowercase()] ?: m.value
            }
        }
    }

    fun runs(text: String): List<Run> {
        val out = ArrayList<Run>()
        var italic = 0
        var bold = 0
        var underline = 0
        fun plain(part: String) {
            if (part.isNotEmpty()) out.add(Run(decode(part), italic > 0, bold > 0, underline > 0))
        }
        fun tag(part: String) {
            if (part == "\n") {
                out.add(Run("\n"))
                return
            }
            val tag = STYLE_TAG.find(part) ?: return
            val step = if (tag.groupValues[1].isEmpty()) 1 else -1
            when (tag.groupValues[2].lowercase()) {
                "i" -> italic = maxOf(0, italic + step)
                "b" -> bold = maxOf(0, bold + step)
                else -> underline = maxOf(0, underline + step)
            }
        }
        var last = 0
        for (m in PARTS.findAll(text)) {
            plain(text.substring(last, m.range.first))
            tag(m.value)
            last = m.range.last + 1
        }
        plain(text.substring(last))
        return out
    }
}
