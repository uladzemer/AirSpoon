/**
 *  Copyright (C) 2011-2012  Juho Vähä-Herttua
 *
 *  This library is free software; you can redistribute it and/or
 *  modify it under the terms of the GNU Lesser General Public
 *  License as published by the Free Software Foundation; either
 *  version 2.1 of the License, or (at your option) any later version.
 *
 *  This library is distributed in the hope that it will be useful,
 *  but WITHOUT ANY WARRANTY; without even the implied warranty of
 *  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the GNU
 *  Lesser General Public License for more details.
 *
 *==================================================================
 * modified by fduncanh 2021-2023
 */

#include <stdlib.h>
#include <string.h>
#include <assert.h>
#include <math.h>
#include <stdio.h>
#include <stdbool.h>
#include <stdint.h>
#include <time.h>

#include "raop_buffer.h"
#include "raop_rtp.h"

#include "crypto.h"
#include "compat.h"
#include "stream.h"
#include "global.h"
#include "utils.h"
#include "byteutils.h"

#define RAOP_BUFFER_LENGTH 256

/* AirSpoon: сколько ждать недостающий пакет, пока за ним уже лежат принятые.
 * Раньше при потере пакета во всех трёх копиях и безответном ретрансмите
 * буфер стоял, пока не наберётся 256 пакетов (~2 с), и потом отдавал пачку:
 * «звук не идёт, потом обрывки». 100 мс — несколько повторов запроса
 * ретрансмита по Wi-Fi (он идёт на каждый принятый пакет); после этого дыру
 * пропускаем, а таймлайн плеера заполнит её тишиной нужной длины. */
#define RAOP_BUFFER_HOLE_WAIT_NS 100000000ULL

/* AirSpoon: счётчики для сводки звука (пишется раз в 5 с в audio_engine.cpp). */
static uint32_t holes_skipped_total;
static uint32_t resend_requests_total;

void raop_buffer_audio_stats(uint32_t *holes_skipped, uint32_t *resend_requests)
{
    *holes_skipped = __atomic_load_n(&holes_skipped_total, __ATOMIC_RELAXED);
    *resend_requests = __atomic_load_n(&resend_requests_total, __ATOMIC_RELAXED);
}

void raop_buffer_note_resend_request(void)
{
    __atomic_add_fetch(&resend_requests_total, 1, __ATOMIC_RELAXED);
}

static uint64_t
monotonic_ns(void)
{
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (uint64_t) ts.tv_sec * 1000000000ULL + (uint64_t) ts.tv_nsec;
}

/* AirSpoon: диагностика дыр и пауз приёма (только логирование, поведение не меняет).
 * rx_*: интервалы между UDP-датаграммами звука. Потребитель событий один: поток
 * RAOP (audio_engine.cpp забирает их через raop_buffer_pop_hole_event). */
static uint64_t rx_prev_ns, rx_last_gap_ns, rx_max_gap_ns;

void raop_buffer_note_rx(void)
{
    uint64_t now = monotonic_ns();
    uint64_t prev = __atomic_load_n(&rx_prev_ns, __ATOMIC_RELAXED);
    if (prev) {
        uint64_t gap = now - prev;
        __atomic_store_n(&rx_last_gap_ns, gap, __ATOMIC_RELAXED);
        if (gap > __atomic_load_n(&rx_max_gap_ns, __ATOMIC_RELAXED)) {
            __atomic_store_n(&rx_max_gap_ns, gap, __ATOMIC_RELAXED);
        }
    }
    __atomic_store_n(&rx_prev_ns, now, __ATOMIC_RELAXED);
}

uint32_t raop_buffer_take_max_rx_gap_ms(void)
{
    uint64_t gap = __atomic_exchange_n(&rx_max_gap_ns, 0, __ATOMIC_RELAXED);
    return (uint32_t) (gap / 1000000ULL);
}

#define HOLE_EV_OPEN   1
#define HOLE_EV_FILLED 2
#define HOLE_EV_SKIP   3
#define HOLE_EV_CAP    64

typedef struct {
    int type;
    uint64_t t_ns;
    unsigned short seq;
    unsigned short count;
    uint32_t ms;
} hole_event_t;

static hole_event_t hole_ev[HOLE_EV_CAP];
static uint32_t hole_ev_w, hole_ev_r, hole_ev_dropped;

static void
hole_event_push(int type, unsigned short seq, unsigned short count, uint32_t ms)
{
    uint32_t w = __atomic_load_n(&hole_ev_w, __ATOMIC_RELAXED);
    uint32_t r = __atomic_load_n(&hole_ev_r, __ATOMIC_ACQUIRE);
    if (w - r >= HOLE_EV_CAP) {
        __atomic_add_fetch(&hole_ev_dropped, 1, __ATOMIC_RELAXED);
        return;
    }
    hole_event_t *e = &hole_ev[w % HOLE_EV_CAP];
    e->type = type;
    e->t_ns = monotonic_ns();
    e->seq = seq;
    e->count = count;
    e->ms = ms;
    __atomic_store_n(&hole_ev_w, w + 1, __ATOMIC_RELEASE);
}

/* формирует одну строку лога; 0 = очередь пуста. t: CLOCK_MONOTONIC в мс (как monoNs()) */
int raop_buffer_pop_hole_event(char *line, size_t cap)
{
    uint32_t r = __atomic_load_n(&hole_ev_r, __ATOMIC_RELAXED);
    uint32_t w = __atomic_load_n(&hole_ev_w, __ATOMIC_ACQUIRE);
    if (r == w) return 0;
    hole_event_t e = hole_ev[r % HOLE_EV_CAP];
    __atomic_store_n(&hole_ev_r, r + 1, __ATOMIC_RELEASE);
    long long t = (long long) (e.t_ns / 1000000ULL);
    switch (e.type) {
    case HOLE_EV_OPEN:
        snprintf(line, cap, "HOLE_OPEN t=%lld seq=%u count=%u rxGapBeforeMs=%u", t, e.seq, e.count, e.ms);
        break;
    case HOLE_EV_FILLED:
        snprintf(line, cap, "HOLE_FILLED t=%lld seq=%u openMs=%u", t, e.seq, e.ms);
        break;
    default:
        snprintf(line, cap, "HOLE_SKIP t=%lld seq=%u count=%u openMs=%u", t, e.seq, e.count, e.ms);
        break;
    }
    return 1;
}

uint32_t raop_buffer_take_hole_events_dropped(void)
{
    return __atomic_exchange_n(&hole_ev_dropped, 0, __ATOMIC_RELAXED);
}

/* новая сессия: не печатать чужие события и не считать паузу между сессиями паузой приёма.
 * Зовётся из потока RAOP, он же единственный потребитель очереди. */
static void
diag_reset(void)
{
    __atomic_store_n(&rx_prev_ns, 0, __ATOMIC_RELAXED);
    __atomic_store_n(&rx_last_gap_ns, 0, __ATOMIC_RELAXED);
    __atomic_store_n(&rx_max_gap_ns, 0, __ATOMIC_RELAXED);
    __atomic_store_n(&hole_ev_r, __atomic_load_n(&hole_ev_w, __ATOMIC_ACQUIRE), __ATOMIC_RELEASE);
    __atomic_store_n(&hole_ev_dropped, 0, __ATOMIC_RELAXED);
}

typedef struct {
    /* Data available */
    int filled;

    uint64_t packet_arrival_time;

    /* RTP header */
    unsigned short seqnum;
    uint32_t rtp_timestamp;

    /* Payload data */
    unsigned int payload_size;
    void *payload_data;
} raop_buffer_entry_t;

struct raop_buffer_s {
    logger_t *logger;
    /* AES CTX used for decryption */
    aes_ctx_t *aes_ctx;

    /* First and last seqnum */
    int is_empty;
    unsigned short first_seqnum;
    unsigned short last_seqnum;

    /* AirSpoon: на дыру, начинающуюся с resend_seq, уже отправлен запрос
     * ретрансмита (resend_asked). Пока запроса не было, дыру не пропускаем:
     * потерянный пакет должен быть запрошен хотя бы один раз. */
    int resend_asked;
    unsigned short resend_seq;

    /* AirSpoon: диагностика: открытая дыра (для HOLE_OPEN/FILLED/SKIP) */
    int hole_open_valid;
    unsigned short hole_open_seq;
    uint64_t hole_open_ns;

    /* RTP buffer entries */
    raop_buffer_entry_t entries[RAOP_BUFFER_LENGTH];
};

raop_buffer_t *
raop_buffer_init(logger_t *logger,
                 const unsigned char *aeskey,
                 const unsigned char *aesiv)
{
    assert(aeskey);
    assert(aesiv);
    raop_buffer_t *raop_buffer = (raop_buffer_t *) calloc(1, sizeof(raop_buffer_t));
    if (!raop_buffer) {
        return NULL;
    }
    raop_buffer->logger = logger;
    // Need to be initialized internally
    raop_buffer->aes_ctx = aes_cbc_init(aeskey, aesiv, AES_DECRYPT);

    for (int i = 0; i < RAOP_BUFFER_LENGTH; i++) {
        raop_buffer_entry_t *entry = &raop_buffer->entries[i];
        entry->payload_data = NULL;
        entry->payload_size = 0;
    }

    raop_buffer->is_empty = 1;
    diag_reset(); /* AirSpoon: новая сессия, без чужих событий и ложного гэпа */

    return raop_buffer;
}

void
raop_buffer_destroy(raop_buffer_t *raop_buffer)
{
    for (int i = 0; i < RAOP_BUFFER_LENGTH; i++) {
        raop_buffer_entry_t *entry = &raop_buffer->entries[i];
        if (entry->payload_data != NULL) {
            free(entry->payload_data);
        }
    }

    if (raop_buffer) {
        aes_cbc_destroy(raop_buffer->aes_ctx);
        free(raop_buffer);
    }

}

static short
seqnum_cmp(unsigned short s1, unsigned short s2)
{
    return (s1 - s2);
}

int
raop_buffer_decrypt(raop_buffer_t *raop_buffer, unsigned char *data, unsigned char* output, unsigned int payload_size, unsigned int *outputlen)
{
    assert(raop_buffer);
    if (DECRYPTION_TEST) {
        char *str = utils_data_to_string(data,12,12);
        logger_log(raop_buffer->logger, LOGGER_INFO, "encrypted 12 byte header %s", str);
        free(str);
        if (payload_size) {
            str = utils_data_to_string(&data[12],16,16);
            logger_log(raop_buffer->logger, LOGGER_INFO, "len %d before decryption:\n%s", payload_size, str);
            free(str);
        }
    }
    int encryptedlen = payload_size / 16*16;
    memset(output, 0, payload_size);

    aes_cbc_decrypt(raop_buffer->aes_ctx, &data[12], output, encryptedlen);
    aes_cbc_reset(raop_buffer->aes_ctx);

    memcpy(output + encryptedlen, &data[12 + encryptedlen], payload_size - encryptedlen);
    *outputlen = payload_size;
    if (payload_size &&  DECRYPTION_TEST){
        switch (output[0]) {
        case 0x8c:
        case 0x8d:
        case 0x8e:
        case 0x80:
        case 0x81:
        case 0x82:
        case 0x20:
	    break;
        default:
            logger_log(raop_buffer->logger, LOGGER_INFO, "***ERROR AUDIO FRAME  IS NOT AAC_ELD OR ALAC");
	    break;
        }
        if (DECRYPTION_TEST == 2) {
            logger_log(raop_buffer->logger, LOGGER_INFO, "decrypted audio frame, len = %d", *outputlen);
            char *str = utils_data_to_string(output,payload_size,16);
            logger_log(raop_buffer->logger, LOGGER_INFO,"%s",str);
            free(str);
        } else {
            char *str = utils_data_to_string(output,16,16);
            logger_log(raop_buffer->logger, LOGGER_INFO, "%d after  \n%s", payload_size, str);
            free(str);
        }
    }
    return 1;
}

int
raop_buffer_enqueue(raop_buffer_t *raop_buffer, unsigned char *data, unsigned short datalen, int use_seqnum) {
    unsigned char empty_packet_marker[] = { 0x00, 0x68, 0x34, 0x00 };
    assert(raop_buffer);

    /* Check packet data length is valid */
    if (datalen < 12 || datalen > RAOP_PACKET_LEN) {
        return -1;
    }
    /* before time is synchronized, some empty data packets are sent */
    if (datalen == 12 || (datalen == 16 && !memcmp(&data[12], empty_packet_marker, 4))) {
        return 0;
    }
    int payload_size = datalen - 12;

    /* Get correct seqnum for the packet */
    unsigned short seqnum = 0;
    if (use_seqnum) {
        seqnum = byteutils_get_short_be(data, 2);
    } else {
        seqnum = raop_buffer->first_seqnum;
    }

    /* If this packet is too late, just skip it */
    if (!raop_buffer->is_empty && seqnum_cmp(seqnum, raop_buffer->first_seqnum) < 0) {
        return 0;
    }

    /* Check that there is always space in the buffer, otherwise flush */
    if (seqnum_cmp(seqnum, raop_buffer->first_seqnum + RAOP_BUFFER_LENGTH) >= 0) {
        raop_buffer_flush(raop_buffer, seqnum);
    }

    /* Get entry corresponding our seqnum */
    raop_buffer_entry_t *entry = &raop_buffer->entries[seqnum % RAOP_BUFFER_LENGTH];
    if (entry->filled && seqnum_cmp(entry->seqnum, seqnum) == 0) {
        /* Packet resend, we can safely ignore */
        return 0;
    }

    /* Update the raop_buffer entry header */
    entry->seqnum = seqnum;
    entry->rtp_timestamp = byteutils_get_int_be(data, 4);
    entry->packet_arrival_time = monotonic_ns(); /* AirSpoon: для пропуска дыр */
    entry->filled = 1;

    entry->payload_data = malloc(payload_size);
    int decrypt_ret = raop_buffer_decrypt(raop_buffer, data, entry->payload_data, payload_size, &entry->payload_size);
    assert(decrypt_ret >= 0);
    assert((int) entry->payload_size <= payload_size);

    /* Update the raop_buffer seqnums */
    if (raop_buffer->is_empty) {
        raop_buffer->first_seqnum = seqnum;
        raop_buffer->last_seqnum = seqnum;
        raop_buffer->is_empty = 0;
    }
    if (seqnum_cmp(seqnum, raop_buffer->last_seqnum) > 0) {
        raop_buffer->last_seqnum = seqnum;
    }
    return 1;
}

void *
raop_buffer_dequeue(raop_buffer_t *raop_buffer, unsigned int *length, uint32_t *rtp_timestamp, unsigned short *seqnum, int no_resend) {
    assert(raop_buffer);

    /* Calculate number of entries in the current buffer */
    short entry_count = seqnum_cmp(raop_buffer->last_seqnum, raop_buffer->first_seqnum)+1;

    /* Cannot dequeue from empty buffer */
    if (raop_buffer->is_empty || entry_count <= 0) {
        return NULL;
    }

    /* Get the first buffer entry for inspection */
    raop_buffer_entry_t *entry = &raop_buffer->entries[raop_buffer->first_seqnum % RAOP_BUFFER_LENGTH];
    if (no_resend) {
        /* If we do no resends, always return the first entry */
    } else if (!entry->filled) {
        /* Check how much we have space left in the buffer */
        if (entry_count < RAOP_BUFFER_LENGTH) {
            /* AirSpoon: ждём ретрансмит не дольше RAOP_BUFFER_HOLE_WAIT_NS с
             * прибытия самого раннего принятого пакета за дырой. Дальше дыру
             * пропускаем и отдаём этот пакет (запрос ретрансмита при этом не
             * отменяется: raop_buffer_handle_resends работает как раньше). */
            unsigned short probe = raop_buffer->first_seqnum + 1;
            raop_buffer_entry_t *waiting = NULL;
            while (seqnum_cmp(raop_buffer->last_seqnum, probe) >= 0) {
                raop_buffer_entry_t *candidate = &raop_buffer->entries[probe % RAOP_BUFFER_LENGTH];
                if (candidate->filled) {
                    waiting = candidate;
                    break;
                }
                probe++;
            }
            int asked = raop_buffer->resend_asked && raop_buffer->resend_seq == raop_buffer->first_seqnum;
            if (!waiting || !asked || monotonic_ns() - waiting->packet_arrival_time < RAOP_BUFFER_HOLE_WAIT_NS) {
                /* Return nothing and hope resend gets on time */
                return NULL;
            }
            /* AirSpoon: диагностика закрытия дыры пропуском */
            if (raop_buffer->hole_open_valid && raop_buffer->hole_open_seq == raop_buffer->first_seqnum) {
                hole_event_push(HOLE_EV_SKIP, raop_buffer->first_seqnum,
                                (unsigned short) (probe - raop_buffer->first_seqnum),
                                (uint32_t) ((monotonic_ns() - raop_buffer->hole_open_ns) / 1000000ULL));
                raop_buffer->hole_open_valid = 0;
            }
            /* probe — первый принятый пакет за дырой: пропускаем всё до него */
            __atomic_add_fetch(&holes_skipped_total, (uint32_t) (unsigned short) (probe - raop_buffer->first_seqnum), __ATOMIC_RELAXED);
            raop_buffer->first_seqnum = probe;
            raop_buffer->resend_asked = 0;
            entry = waiting;
        } else {
            /* Risk of buffer overrun, return empty buffer */
        }
    }

    /* Update buffer and validate entry */
    unsigned short cur_seq = raop_buffer->first_seqnum; /* AirSpoon: для диагностики дыр */
    raop_buffer->first_seqnum += 1;
    if (raop_buffer->hole_open_valid && raop_buffer->hole_open_seq == cur_seq) {
        uint32_t open_ms = (uint32_t) ((monotonic_ns() - raop_buffer->hole_open_ns) / 1000000ULL);
        hole_event_push(entry->filled ? HOLE_EV_FILLED : HOLE_EV_SKIP, cur_seq, 1, open_ms);
        raop_buffer->hole_open_valid = 0;
    }
    if (!entry->filled) {
        __atomic_add_fetch(&holes_skipped_total, 1, __ATOMIC_RELAXED);
        return NULL;
    }
    entry->filled = 0;

    /* Return entry payload buffer */
    *rtp_timestamp = entry->rtp_timestamp;
    *seqnum = entry->seqnum;
    *length = entry->payload_size;
    entry->payload_size = 0;
    void* data = entry->payload_data;
    entry->payload_data = NULL;
    return data;
}

void raop_buffer_handle_resends(raop_buffer_t *raop_buffer, raop_resend_cb_t resend_cb, void *opaque) {
    assert(raop_buffer);
    assert(resend_cb);

    if (seqnum_cmp(raop_buffer->first_seqnum, raop_buffer->last_seqnum) < 0) {
        unsigned short seqnum, count = 0;
        logger_log(raop_buffer->logger, LOGGER_DEBUG, "raop_buffer_handle_resends first_seqnum=%u last seqnum=%u",
                   raop_buffer->first_seqnum, raop_buffer->last_seqnum);
        for (seqnum = raop_buffer->first_seqnum; seqnum_cmp(seqnum, raop_buffer->last_seqnum) < 0; seqnum++) {
            raop_buffer_entry_t *entry = &raop_buffer->entries[seqnum % RAOP_BUFFER_LENGTH];
            if (entry->filled) {
                break;
            }
	    count++;
        }
        if (count){
            /* AirSpoon: первая просьба о ретрансмите для этого first_seqnum: дыра открыта */
            if (!raop_buffer->hole_open_valid || raop_buffer->hole_open_seq != raop_buffer->first_seqnum) {
                raop_buffer->hole_open_valid = 1;
                raop_buffer->hole_open_seq = raop_buffer->first_seqnum;
                raop_buffer->hole_open_ns = monotonic_ns();
                hole_event_push(HOLE_EV_OPEN, raop_buffer->first_seqnum, count,
                                (uint32_t) (__atomic_load_n(&rx_last_gap_ns, __ATOMIC_RELAXED) / 1000000ULL));
            }
            raop_buffer->resend_asked = 1;
            raop_buffer->resend_seq = raop_buffer->first_seqnum;
            resend_cb(opaque, raop_buffer->first_seqnum, count);
        }
    }
}

void raop_buffer_flush(raop_buffer_t *raop_buffer, int next_seq) {
    assert(raop_buffer);

    raop_buffer->resend_asked = 0;
    raop_buffer->hole_open_valid = 0;
    for (int i = 0; i < RAOP_BUFFER_LENGTH; i++) {
        if (raop_buffer->entries[i].payload_data) {
            free(raop_buffer->entries[i].payload_data);
            raop_buffer->entries[i].payload_data = NULL;   
            raop_buffer->entries[i].payload_size = 0;
        }
        raop_buffer->entries[i].filled = 0;
    }
    if (next_seq < 0 || next_seq > 0xffff) {
        raop_buffer->is_empty = 1;
        diag_reset(); /* AirSpoon: полный сброс буфера = конец сессии */
    } else {
        raop_buffer->first_seqnum = next_seq;
        raop_buffer->last_seqnum = next_seq - 1;
    }
}
