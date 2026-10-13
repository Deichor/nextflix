package com.nextflix.tv.core.session

object ServerAddress {
    const val DEFAULT = "http://nextflix.local:8252"

    /** Tried on first run in this order (not every TV resolves .local names), like the webOS app. */
    val CANDIDATES = listOf(DEFAULT, "http://192.168.1.221:8252", "http://127.0.0.1:8252")

    private val PARTS = Regex("^(https?://)([^/:]+)(:\\d+)?(/.*)?$", RegexOption.IGNORE_CASE)

    /** "192.168.1.5" -> "http://192.168.1.5:8252"; keeps an explicit scheme, port and path; no trailing slash. */
    fun normalize(input: String): String {
        var s = input.trim().trimEnd('/')
        if (!s.startsWith("http://", ignoreCase = true) && !s.startsWith("https://", ignoreCase = true)) s = "http://$s"
        val m = PARTS.matchEntire(s)
        if (m != null && m.groupValues[3].isEmpty() && m.groupValues[1].equals("http://", ignoreCase = true)) {
            s = "${m.groupValues[1]}${m.groupValues[2]}:8252${m.groupValues[4]}"
        }
        return s
    }
}
