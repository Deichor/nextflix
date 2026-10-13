package com.nextflix.tv.core.net

import com.nextflix.tv.core.session.Session
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.suspendCancellableCoroutine
import kotlinx.coroutines.withContext
import kotlinx.serialization.DeserializationStrategy
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.serializer
import okhttp3.Call
import okhttp3.Callback
import okhttp3.HttpUrl
import okhttp3.HttpUrl.Companion.toHttpUrl
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull
import okhttp3.Interceptor
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.Response
import java.io.IOException
import java.util.concurrent.TimeUnit
import kotlin.coroutines.resume
import kotlin.coroutines.resumeWithException

/**
 * JSON client for the Nextflix server (docs/API.md), like tv/webos/src/lib/http.ts.
 *
 * - Paths are server paths ("/api/profiles"); the base URL is the saved server address.
 * - Every /api request to that server carries `X-Lang` (profile language) and `X-User-ID` (profile id).
 *   The app sends no Origin and no Sec-Fetch-Site, so the server's CORS / cross-site guards do not apply;
 *   its Host guard accepts LAN addresses and nextflix.local.
 * - Query maps skip null values. Failures throw [ApiException]; cancellation propagates as usual.
 */
class ApiClient internal constructor(
    private val session: Session,
    /** Shared client (connection pool, timeouts: resolve can take ~60 s). Media3 can reuse it via OkHttpDataSource. */
    val okHttp: OkHttpClient,
    val json: Json,
    private val scope: CoroutineScope,
) {
    /** Absolute URL of a server path with query parameters (nulls left out). Throws [ApiException.NoServer]. */
    fun url(path: String, query: Map<String, Any?> = emptyMap()): HttpUrl {
        val base = session.server ?: throw ApiException.NoServer()
        val builder = "$base$path".toHttpUrl().newBuilder()
        query.forEach { (k, v) -> if (v != null) builder.addQueryParameter(k, v.toString()) }
        return builder.build()
    }

    /** GET and decode with [deserializer]. */
    suspend fun <T> get(path: String, deserializer: DeserializationStrategy<T>, query: Map<String, Any?> = emptyMap()): T =
        decode(deserializer, call("GET", path, query))

    /** GET and decode into T: `api.get<List<Profile>>("/api/profiles")`. */
    suspend inline fun <reified T> get(path: String, query: Map<String, Any?> = emptyMap()): T =
        get(path, serializer<T>(), query)

    /** GET as loose JSON (JsonNull for an empty answer). */
    suspend fun getJson(path: String, query: Map<String, Any?> = emptyMap()): JsonElement = call("GET", path, query)

    /** POST a JSON body (null: empty body) and decode the answer. */
    suspend inline fun <reified T> post(path: String, body: JsonElement? = null, query: Map<String, Any?> = emptyMap()): T =
        decode(serializer<T>(), call("POST", path, query, body))

    /** PUT a JSON body and decode the answer. */
    suspend inline fun <reified T> put(path: String, body: JsonElement?, query: Map<String, Any?> = emptyMap()): T =
        decode(serializer<T>(), call("PUT", path, query, body))

    suspend fun delete(path: String, query: Map<String, Any?> = emptyMap()) {
        call("DELETE", path, query)
    }

    /** Encodes a @Serializable value as a request body. */
    inline fun <reified B> body(value: B): JsonElement = json.encodeToJsonElement(serializer<B>(), value)

    fun <T> decode(deserializer: DeserializationStrategy<T>, element: JsonElement): T =
        try {
            json.decodeFromJsonElement(deserializer, element)
        } catch (e: IllegalArgumentException) {
            throw ApiException.Decode(e)
        }

    /** Any method; returns the JSON answer (JsonNull for 204 or an empty body). Throws [ApiException.Http] for non-2xx. */
    suspend fun call(method: String, path: String, query: Map<String, Any?> = emptyMap(), body: JsonElement? = null): JsonElement {
        val response = raw(method, path, query, body)
        return response.use { res ->
            val text = withContext(Dispatchers.IO) {
                try {
                    res.body.string()
                } catch (e: IOException) {
                    throw ApiException.Network(e)
                }
            }
            val parsed: JsonElement? = if (text.isBlank()) null else runCatching { json.parseToJsonElement(text) }.getOrNull()
            if (!res.isSuccessful) {
                val error = ((parsed as? JsonObject)?.get("error") as? JsonPrimitive)?.content
                throw ApiException.Http(res.code, error ?: "$path -> ${res.code}", parsed)
            }
            when {
                parsed != null -> parsed
                text.isBlank() -> JsonNull
                else -> throw ApiException.Decode(IllegalStateException("not JSON: ${text.take(80)}"))
            }
        }
    }

    /**
     * The raw response for answers that are not JSON or whose headers matter (text/vtt, 204 + Retry-After).
     * Any status is returned; the caller must close it. Throws [ApiException.Network] / [ApiException.NoServer].
     */
    suspend fun raw(method: String, path: String, query: Map<String, Any?> = emptyMap(), body: JsonElement? = null): Response {
        val requestBody = when {
            body != null -> json.encodeToString(JsonElement.serializer(), body).toRequestBody(JSON_TYPE)
            method == "POST" || method == "PUT" -> ByteArray(0).toRequestBody(null)
            else -> null
        }
        val request = Request.Builder().url(url(path, query)).method(method, requestBody).build()
        return try {
            okHttp.newCall(request).await()
        } catch (e: IOException) {
            throw ApiException.Network(e)
        }
    }

    /** Fire-and-forget POST that outlives the calling screen (release, keepalive). Errors are ignored. */
    fun beacon(path: String, query: Map<String, Any?> = emptyMap()) {
        scope.launch(Dispatchers.IO) {
            runCatching { raw("POST", path, query).close() }
        }
    }

    /**
     * Checks a server address before it is saved: /api/health, then /api/profiles. Returns the server version.
     * [baseUrl] must be normalized (ServerAddress.normalize).
     */
    suspend fun checkServer(baseUrl: String, timeoutMs: Long = 8_000): String {
        val client = okHttp.newBuilder().callTimeout(timeoutMs, TimeUnit.MILLISECONDS).build()
        val base = baseUrl.toHttpUrlOrNull() ?: throw ApiException.Decode(IllegalArgumentException("bad address $baseUrl"))
        suspend fun fetch(path: String): String {
            // Like url(): the path goes after the address, which may carry a prefix
            val url = "$baseUrl$path".toHttpUrlOrNull() ?: throw ApiException.Decode(IllegalArgumentException("bad address $baseUrl"))
            val req = Request.Builder().url(url).header("X-Lang", "en").tag(CleartextHost::class.java, CleartextHost(base.host, base.port)).build()
            val res = try {
                client.newCall(req).await()
            } catch (e: IOException) {
                throw ApiException.Network(e)
            }
            return res.use {
                val text = withContext(Dispatchers.IO) { runCatching { it.body.string() }.getOrDefault("") }
                if (!it.isSuccessful) throw ApiException.Http(it.code, "HTTP ${it.code}", null)
                text
            }
        }
        val health = fetch("/api/health")
        fetch("/api/profiles")
        return runCatching { (json.parseToJsonElement(health).obj?.str("version")).orEmpty() }.getOrDefault("")
    }

    internal companion object {
        val JSON_TYPE = "application/json; charset=utf-8".toMediaType()
    }
}

/** Adds X-Lang (unless ?x_lang= is set) and X-User-ID to /api requests for the configured server (not to other hosts). */
internal class ApiHeadersInterceptor(private val session: Session) : Interceptor {
    override fun intercept(chain: Interceptor.Chain): Response {
        val request = chain.request()
        val state = session.current
        val base = state.server?.toHttpUrlOrNull()
        val url = request.url
        if (base == null || url.host != base.host || url.port != base.port || !url.encodedPath.startsWith(base.encodedPath.trimEnd('/') + "/api/")) {
            return chain.proceed(request)
        }
        val builder = request.newBuilder()
        // ?x_lang= names the language itself (GET /api/home for a profile not picked yet); the header would win
        if (request.header("X-Lang") == null && url.queryParameter("x_lang") == null) builder.header("X-Lang", state.language.code)
        val pid = state.profile?.id
        if (pid != null && request.header("X-User-ID") == null) builder.header("X-User-ID", pid.toString())
        return chain.proceed(builder.build())
    }
}

/** Suspends until the response headers arrive; cancelling the coroutine cancels the call. */
suspend fun Call.await(): Response = suspendCancellableCoroutine { cont ->
    cont.invokeOnCancellation { cancel() }
    enqueue(object : Callback {
        override fun onFailure(call: Call, e: IOException) {
            cont.resumeWithException(e)
        }

        override fun onResponse(call: Call, response: Response) {
            cont.resume(response) { _, _, _ -> response.close() }
        }
    })
}

/** A host the request may reach over plain HTTP besides the saved server (the address being checked in Setup). */
internal class CleartextHost(val host: String, val port: Int)

/**
 * Plain HTTP only to the saved server (its address is the user's LAN IP, so the network security config cannot
 * name it); a redirect or a URL from server data to any other host must use HTTPS. A network interceptor, so it
 * also sees redirects.
 */
internal class CleartextGuard(private val session: Session) : Interceptor {
    override fun intercept(chain: Interceptor.Chain): Response {
        val request = chain.request()
        val url = request.url
        if (!url.isHttps) {
            val server = session.current.server?.toHttpUrlOrNull()
            val extra = request.tag(CleartextHost::class.java)
            val allowed = (server != null && url.host == server.host && url.port == server.port) ||
                (extra != null && url.host == extra.host && url.port == extra.port)
            if (!allowed) throw IOException("cleartext HTTP to ${url.host} blocked")
        }
        return chain.proceed(request)
    }
}
