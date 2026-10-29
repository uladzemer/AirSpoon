package io.github.jqssun.airplay.audio

internal class DacpEndpointState {
    data class Request(val generation: Long, val id: String, val token: String)
    data class Endpoint(val generation: Long, val host: String, val port: Int, val token: String)

    private var generation = 0L
    private var endpoint: Endpoint? = null

    @Synchronized fun update(id: String, token: String): Request {
        endpoint = null
        return Request(++generation, id, token)
    }

    @Synchronized fun resolved(request: Request, host: String, port: Int): Boolean {
        if (request.generation != generation || request.id.isEmpty() || request.token.isEmpty() ||
            host.isEmpty() || port !in 1..65535) return false
        endpoint = Endpoint(generation, host, port, request.token)
        return true
    }

    @Synchronized fun snapshot(): Endpoint? = endpoint
    @Synchronized fun isCurrent(value: Endpoint): Boolean = value.generation == generation
}
