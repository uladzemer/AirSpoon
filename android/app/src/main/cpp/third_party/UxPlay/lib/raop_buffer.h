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
 * modified by fduncanh 2021-23
 */

#ifndef RAOP_BUFFER_H
#define RAOP_BUFFER_H

#include "logger.h"
#include "raop_rtp.h"

typedef struct raop_buffer_s raop_buffer_t;

typedef int (*raop_resend_cb_t)(void *opaque, unsigned short seqno, unsigned short count);

raop_buffer_t *raop_buffer_init(logger_t *logger,
                                const unsigned char *aeskey,
                                const unsigned char *aesiv);
int raop_buffer_enqueue(raop_buffer_t *raop_buffer, unsigned char *data, unsigned short datalen, int use_seqnum);
void *raop_buffer_dequeue(raop_buffer_t *raop_buffer, unsigned int *length, uint32_t *rtp_timestamp, unsigned short *seqnum, int no_resend);
void raop_buffer_handle_resends(raop_buffer_t *raop_buffer, raop_resend_cb_t resend_cb, void *opaque);
void raop_buffer_flush(raop_buffer_t *raop_buffer, int next_seq);

/* AirSpoon: счётчики приёмника звука с запуска процесса (для сводки в logcat):
 * пропущенные дыры (потерянные пакеты, которых не дождались) и отправленные
 * запросы ретрансмита. */
void raop_buffer_audio_stats(uint32_t *holes_skipped, uint32_t *resend_requests);
void raop_buffer_note_resend_request(void);

/* AirSpoon: диагностика приёма звука (только логи). note_rx: на каждую UDP-датаграмму
 * звука; take_max_rx_gap_ms: максимум паузы между датаграммами с прошлого вызова
 * (сбрасывается); pop_hole_event: одна готовая строка HOLE_* (0 = пусто);
 * take_hole_events_dropped: сколько событий потеряно при переполнении очереди. */
void raop_buffer_note_rx(void);
uint32_t raop_buffer_take_max_rx_gap_ms(void);
int raop_buffer_pop_hole_event(char *line, size_t cap);
uint32_t raop_buffer_take_hole_events_dropped(void);

int raop_buffer_decrypt(raop_buffer_t *raop_buffer, unsigned char *data, unsigned char* output,
                        unsigned int datalen, unsigned int *outputlen);
void raop_buffer_destroy(raop_buffer_t *raop_buffer);

#endif
