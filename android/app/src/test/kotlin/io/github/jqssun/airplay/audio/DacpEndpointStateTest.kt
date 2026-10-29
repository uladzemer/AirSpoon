package io.github.jqssun.airplay.audio

import org.junit.Assert.*
import org.junit.Test

class DacpEndpointStateTest {
    @Test fun `late resolve cannot combine old host with a new token`() {
        val state = DacpEndpointState()
        val first = state.update("sender-a", "token-a")
        val second = state.update("sender-b", "token-b")
        assertFalse(state.resolved(first, "192.0.2.1", 1234))
        assertNull(state.snapshot())
        assertTrue(state.resolved(second, "192.0.2.2", 5678))
        assertFalse(state.resolved(first, "192.0.2.1", 1234))
        assertEquals(DacpEndpointState.Endpoint(second.generation, "192.0.2.2", 5678, "token-b"), state.snapshot())
    }

    @Test fun `same sender update clears endpoint and invalidates captured commands`() {
        val state = DacpEndpointState()
        val first = state.update("sender", "old-token")
        state.resolved(first, "192.0.2.1", 1234)
        val captured = state.snapshot()!!
        val second = state.update("sender", "new-token")
        assertNull(state.snapshot())
        assertFalse(state.isCurrent(captured))
        assertEquals("old-token", captured.token)
        assertTrue(state.resolved(second, "192.0.2.2", 5678))
        assertEquals("new-token", state.snapshot()!!.token)
    }

    @Test fun `reset rejects pending resolve and invalid endpoints stay unready`() {
        val state = DacpEndpointState()
        val request = state.update("sender", "token")
        assertFalse(state.resolved(request, "", 1234))
        assertFalse(state.resolved(request, "192.0.2.1", 0))
        assertFalse(state.resolved(request, "192.0.2.1", 65536))
        state.update("", "")
        assertFalse(state.resolved(request, "192.0.2.1", 1234))
        assertNull(state.snapshot())
    }
}
