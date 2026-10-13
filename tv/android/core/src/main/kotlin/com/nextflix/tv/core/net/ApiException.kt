package com.nextflix.tv.core.net

import com.nextflix.tv.core.i18n.CoreStrings
import kotlinx.serialization.json.JsonElement
import java.io.IOException

/** Every failure of an [ApiClient] call. Coroutine cancellation is never wrapped. */
sealed class ApiException(message: String, cause: Throwable? = null) : Exception(message, cause) {
    /** No server address saved yet (first run). */
    class NoServer : ApiException("No server address set")

    /** Connection refused, DNS, timeout, cut connection. */
    class Network(cause: IOException) : ApiException(cause.message ?: cause.javaClass.simpleName, cause)

    /** A non-2xx answer. [message] is the server's `{"error": ...}` text when there is one; [body] the parsed JSON body. */
    class Http(val status: Int, message: String, val body: JsonElement?) : ApiException(message)

    /** The answer was not the expected JSON. */
    class Decode(cause: Throwable) : ApiException(cause.message ?: "bad answer", cause)
}

val Throwable.httpStatus: Int? get() = (this as? ApiException.Http)?.status

/** Short user-facing text for a failure, in the profile language. */
fun Throwable.userMessage(strings: CoreStrings): String = when (this) {
    is ApiException.NoServer -> strings.errorNoServer
    is ApiException.Network -> strings.errorNetwork
    else -> strings.errorGeneric
}
