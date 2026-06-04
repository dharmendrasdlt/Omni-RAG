/**
 * OmniRAG Retrieval UI — app.js
 * Handles SSE streaming from POST /api/search, renders tokens in real-time,
 * and displays polished citation blocks after the answer completes.
 */

'use strict';

// ─── DOM refs ──────────────────────────────────────────────────────────────────
const searchInput  = document.getElementById('search-input');
const searchBtn    = document.getElementById('search-btn');
const btnIcon      = document.getElementById('btn-icon');
const btnLabel     = document.getElementById('btn-label');
const pipeline     = document.getElementById('pipeline');
const stageMsg     = document.getElementById('stage-msg');
const resultsEl    = document.getElementById('results');
const errorBanner  = document.getElementById('error-banner');
const errorText    = document.getElementById('error-text');
const hero         = document.getElementById('hero');
const modalOverlay = document.getElementById('modal-overlay');
const modalBody    = document.getElementById('modal-body');
const modalClose   = document.getElementById('modal-close');

// ─── Stage step refs ──────────────────────────────────────────────────────────
const STAGES = ['embedding', 'retrieval', 'generating'];

// ─── State ────────────────────────────────────────────────────────────────────
let isLoading   = false;
let currentCard = null;   // { el, textEl } for the active answer card

// ─── Pipeline stage helpers ───────────────────────────────────────────────────
function resetPipeline() {
  STAGES.forEach(s => {
    document.getElementById(`icon-${s}`).classList.add('hidden');
    document.getElementById(`check-${s}`).classList.add('hidden');
    const dot = document.getElementById(`dot-${s}`);
    dot.classList.remove('hidden', 'bg-accent', 'bg-emerald-400');
    dot.classList.add('bg-slate-600');

    const step = document.getElementById(`step-${s}`);
    step.classList.remove('border-accent/50', 'text-white', 'border-emerald-500/40', 'text-emerald-300');
    step.classList.add('text-slate-400');
  });
  stageMsg.textContent = '';
}

function activateStage(stage, message) {
  const step = document.getElementById(`step-${stage}`);
  const icon = document.getElementById(`icon-${stage}`);
  const dot  = document.getElementById(`dot-${stage}`);

  dot.classList.add('hidden');
  icon.classList.remove('hidden');
  step.classList.remove('text-slate-400');
  step.classList.add('border-emerald-500/40', 'text-white');

  if (message) stageMsg.textContent = message;
}

function completeStage(stage) {
  const step  = document.getElementById(`step-${stage}`);
  const icon  = document.getElementById(`icon-${stage}`);
  const check = document.getElementById(`check-${stage}`);

  icon.classList.add('hidden');
  check.classList.remove('hidden');
  step.classList.remove('border-emerald-500/40', 'text-white');
  step.classList.add('border-emerald-500/40', 'text-emerald-300');
}

// ─── Button state helpers ─────────────────────────────────────────────────────
function setLoading(on) {
  isLoading = on;
  searchBtn.disabled = on;

  if (on) {
    btnIcon.innerHTML = `
      <svg class="w-4 h-4 animate-spin" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5">
        <path d="M21 12a9 9 0 1 1-6.219-8.56"/>
      </svg>`;
    btnLabel.textContent = 'Searching…';
  } else {
    btnIcon.innerHTML = `
      <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round">
        <circle cx="11" cy="11" r="8"/><path d="m21 21-4.35-4.35"/>
      </svg>`;
    btnLabel.textContent = 'Search';
  }
}

// ─── Error banner ─────────────────────────────────────────────────────────────
function showError(message) {
  errorText.textContent = message;
  errorBanner.classList.remove('hidden');
}

function hideError() {
  errorBanner.classList.add('hidden');
  errorText.textContent = '';
}

// ─── Chat card creation ───────────────────────────────────────────────────────
function createAnswerCard(query) {
  const card = document.createElement('div');
  card.className = 'chat-entry rounded-2xl border border-slate-800 bg-slate-900/50 p-5 shadow-glow';

  card.innerHTML = `
    <!-- Query bubble -->
    <div class="pb-4 border-b border-slate-800">
      <div class="flex items-start gap-3">
        <div class="w-7 h-7 rounded-full bg-slate-900 border border-slate-700 flex items-center justify-center flex-shrink-0 mt-0.5">
          <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="#94a3b8" stroke-width="2.2" stroke-linecap="round"><circle cx="12" cy="8" r="4"/><path d="M4 20c0-4 3.6-7 8-7s8 3 8 7"/></svg>
        </div>
        <p class="text-sm text-white font-medium leading-relaxed query-text"></p>
      </div>
    </div>

    <!-- Answer area -->
    <div class="pt-4">
      <div class="flex items-start gap-3">
        <!-- AI avatar -->
        <div class="w-7 h-7 rounded-full bg-emerald-500/20 border border-emerald-500/40 flex items-center justify-center flex-shrink-0 mt-0.5">
          <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="#10b981" stroke-width="2.2" stroke-linecap="round"><path d="M12 2a7 7 0 0 1 7 7c0 5-7 13-7 13S5 14 5 9a7 7 0 0 1 7-7z"/><circle cx="12" cy="9" r="2.5"/></svg>
        </div>
        <div class="flex-1 min-w-0">
          <p class="text-xs font-semibold text-emerald-400 mb-2">OmniRAG Answer</p>
          <div id="answer-text" class="text-sm text-slate-200 cursor"></div>
        </div>
      </div>

      <!-- Citation section (hidden initially) -->
      <div class="citation-section hidden mt-5 ml-10">
        <div class="flex items-center gap-2 mb-3">
          <div class="h-px flex-1 bg-slate-800"></div>
          <span class="text-xs text-slate-500 font-medium px-2">Sources</span>
          <div class="h-px flex-1 bg-slate-800"></div>
        </div>
        <div class="citation-cards grid gap-2"></div>
      </div>
    </div>
  `;

  // Set query text safely
  card.querySelector('.query-text').textContent = query;

  resultsEl.prepend(card);
  return {
    el:         card,
    textEl:     card.querySelector('#answer-text'),
    citSection: card.querySelector('.citation-section'),
    citCards:   card.querySelector('.citation-cards'),
  };
}

// ─── Source citation card ─────────────────────────────────────────────────────
function renderCitationCard(src, index) {
  const scorePercent = Math.round(src.score * 100);
  const scoreColor   = scorePercent >= 80 ? 'text-emerald-400' : scorePercent >= 60 ? 'text-amber-400' : 'text-slate-400';

  const card = document.createElement('div');
  card.className = 'source-card rounded-xl border border-slate-800 bg-slate-950/40 p-4 cursor-pointer';
  card.innerHTML = `
    <div class="flex items-start justify-between gap-3 mb-2">
      <div class="flex items-center gap-2 min-w-0">
        <!-- Doc icon -->
        <div class="w-7 h-7 rounded-lg bg-emerald-500/15 border border-emerald-500/25 flex items-center justify-center flex-shrink-0">
          <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="#10b981" stroke-width="2" stroke-linecap="round">
            <path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/>
            <polyline points="14 2 14 8 20 8"/><line x1="16" y1="13" x2="8" y2="13"/>
            <line x1="16" y1="17" x2="8" y2="17"/><polyline points="10 9 9 9 8 9"/>
          </svg>
        </div>
        <div class="min-w-0">
          <p class="text-xs font-semibold text-slate-200 truncate">Source ${index + 1}</p>
          <p class="text-xs text-slate-500 font-mono truncate" title="${src.source_file_id}">
            ${src.source_file_id ? src.source_file_id.substring(0, 24) + (src.source_file_id.length > 24 ? '…' : '') : '—'}
          </p>
        </div>
      </div>
      <span class="score-badge text-xs font-semibold ${scoreColor} flex-shrink-0">${scorePercent}%</span>
    </div>

    <!-- Metadata chips -->
    <div class="flex flex-wrap items-center gap-1.5 mb-2.5">
      <span class="px-2 py-0.5 rounded-md bg-slate-900/60 border border-slate-800 text-xs text-slate-400">
        Ch. ${src.chapter || '—'}
      </span>
      <span class="px-2 py-0.5 rounded-md bg-slate-900/60 border border-slate-800 text-xs text-slate-400">
        Pg. ${src.page_number || '—'}
      </span>
      <span class="px-2 py-0.5 rounded-md bg-slate-900/60 border border-slate-800 text-xs text-slate-500 font-mono">
        ${src.id ? src.id.split('_chunk_')[1] ? 'chunk #' + src.id.split('_chunk_')[1] : src.id.substring(0, 12) : '—'}
      </span>
    </div>

    <!-- Snippet -->
    <p class="text-xs text-slate-400 leading-relaxed line-clamp-2 snippet"></p>

    <!-- View source link -->
    <button class="view-source mt-2.5 flex items-center gap-1 text-xs text-emerald-400 hover:text-emerald-300 transition-colors font-medium">
      <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/><polyline points="15 3 21 3 21 9"/><line x1="10" y1="14" x2="21" y2="3"/></svg>
      📎 View Source Document
    </button>
  `;

  // Set snippet text safely
  const snippet = src.text_content || '';
  card.querySelector('.snippet').textContent = snippet.length > 140 ? snippet.substring(0, 140) + '…' : snippet;

  // View source button opens modal
  card.querySelector('.view-source').addEventListener('click', e => {
    e.stopPropagation();
    openSourceModal(src, index);
  });

  card.addEventListener('click', () => openSourceModal(src, index));

  return card;
}

// ─── Source modal ─────────────────────────────────────────────────────────────
function openSourceModal(src, index) {
  const scorePercent = Math.round(src.score * 100);

  modalBody.innerHTML = `
    <div class="flex items-center gap-3 mb-4">
      <div class="w-10 h-10 rounded-xl bg-emerald-500/15 border border-emerald-500/30 flex items-center justify-center">
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="#10b981" stroke-width="1.8" stroke-linecap="round">
          <path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/>
          <polyline points="14 2 14 8 20 8"/><line x1="16" y1="13" x2="8" y2="13"/>
          <line x1="16" y1="17" x2="8" y2="17"/>
        </svg>
      </div>
      <div>
        <p class="text-xs text-slate-500 mb-0.5">Source ${index + 1} of 3</p>
        <p class="font-semibold text-white text-sm">Document ID</p>
      </div>
    </div>

    <div class="space-y-3">
      <div class="bg-slate-900/60 rounded-lg p-3 border border-slate-800">
        <p class="text-xs text-slate-500 mb-1 font-medium">Document ID</p>
        <p class="text-xs font-mono text-slate-200 break-all">${escHtml(src.source_file_id || '—')}</p>
      </div>
      <div class="grid grid-cols-3 gap-2">
        <div class="bg-slate-900/60 rounded-lg p-3 border border-slate-800 text-center">
          <p class="text-xs text-slate-500 mb-1">Chapter</p>
          <p class="text-sm font-semibold text-white">${src.chapter || '—'}</p>
        </div>
        <div class="bg-slate-900/60 rounded-lg p-3 border border-slate-800 text-center">
          <p class="text-xs text-slate-500 mb-1">Page</p>
          <p class="text-sm font-semibold text-white">${src.page_number || '—'}</p>
        </div>
        <div class="bg-slate-900/60 rounded-lg p-3 border border-slate-800 text-center">
          <p class="text-xs text-slate-500 mb-1">Score</p>
          <p class="text-sm font-semibold text-emerald-400">${scorePercent}%</p>
        </div>
      </div>
      <div class="bg-slate-900/60 rounded-lg p-3 border border-slate-800">
        <p class="text-xs text-slate-500 mb-2 font-medium">Matched Text Content</p>
        <p class="text-xs text-slate-300 leading-relaxed">${escHtml(src.text_content || 'No content available.')}</p>
      </div>
      <div class="bg-amber-500/5 border border-amber-500/20 rounded-lg p-3">
        <p class="text-xs text-amber-400/80">
          📎 This chunk is stored in <strong>MongoDB GridFS</strong> with ID <code class="font-mono text-amber-300">${escHtml(src.source_file_id || '—')}</code>.
          Use the ingestion service UI to retrieve the original PDF.
        </p>
      </div>
    </div>
  `;

  modalOverlay.classList.remove('hidden');
}

function closeModal() {
  modalOverlay.classList.add('hidden');
}

modalClose.addEventListener('click', closeModal);
modalOverlay.addEventListener('click', e => { if (e.target === modalOverlay) closeModal(); });
document.addEventListener('keydown', e => { if (e.key === 'Escape') closeModal(); });

// ─── SSE chunk parser ─────────────────────────────────────────────────────────
/**
 * Parses raw SSE bytes from a fetch ReadableStream.
 * Returns an array of { type, data } objects.
 */
function parseSSEChunk(raw) {
  const events = [];
  const lines  = raw.split('\n');
  let type = null;
  let data = null;

  for (const line of lines) {
    if (line.startsWith('event: ')) {
      type = line.slice(7).trim();
    } else if (line.startsWith('data: ')) {
      data = line.slice(6).trim();
    } else if (line === '' && type && data) {
      try {
        events.push({ type, payload: JSON.parse(data) });
      } catch {
        events.push({ type, payload: data });
      }
      type = null;
      data = null;
    }
  }
  return events;
}

// ─── Main search handler ──────────────────────────────────────────────────────
async function runSearch() {
  const query = searchInput.value.trim();
  if (!query || isLoading) return;

  // Reset UI
  hideError();
  setLoading(true);
  pipeline.classList.remove('hidden');
  hero.classList.add('hidden');
  resetPipeline();

  // Create answer card
  currentCard = createAnswerCard(query);
  let fullAnswer = '';

  try {
    const resp = await fetch('/api/search', {
      method:  'POST',
      headers: { 'Content-Type': 'application/json' },
      body:    JSON.stringify({ query }),
    });

    if (!resp.ok) {
      const errBody = await resp.json().catch(() => ({ error: `HTTP ${resp.status}` }));
      throw new Error(errBody.error || `HTTP ${resp.status}`);
    }

    const reader  = resp.body.getReader();
    const decoder = new TextDecoder();
    let   buffer  = '';

    while (true) {
      const { done, value } = await reader.read();
      if (done) break;

      buffer += decoder.decode(value, { stream: true });

      // Process complete SSE blocks (terminated by double newline)
      const blocks = buffer.split('\n\n');
      buffer = blocks.pop(); // keep incomplete trailing block

      for (const block of blocks) {
        if (!block.trim()) continue;
        const events = parseSSEChunk(block + '\n\n');

        for (const { type, payload } of events) {
          handleSSEEvent(type, payload, query, currentCard, text => { fullAnswer += text; });
        }
      }
    }

    // Process any remaining buffer
    if (buffer.trim()) {
      const events = parseSSEChunk(buffer);
      for (const { type, payload } of events) {
        handleSSEEvent(type, payload, query, currentCard, text => { fullAnswer += text; });
      }
    }

  } catch (err) {
    showError(err.message || 'An unexpected error occurred.');
    // Remove cursor from card
    if (currentCard) {
      currentCard.textEl.classList.remove('cursor');
    }
    console.error('[OmniRAG] fetch error:', err);
  } finally {
    setLoading(false);
    // Ensure cursor is removed
    if (currentCard) {
      currentCard.textEl.classList.remove('cursor');
    }
    // Complete all stages visually
    STAGES.forEach(completeStage);
    stageMsg.textContent = 'Complete';
    // Clear input and refocus for next query
    searchInput.value = '';
    searchInput.focus();
    // Scroll input into view
    searchInput.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
  }
}

// ─── SSE event dispatcher ─────────────────────────────────────────────────────
function handleSSEEvent(type, payload, query, card, appendText) {
  switch (type) {

    case 'stage': {
      const s = payload.stage;
      if (STAGES.includes(s)) {
        // Complete previous stages
        const idx = STAGES.indexOf(s);
        STAGES.slice(0, idx).forEach(completeStage);
        activateStage(s, payload.message);
      }
      break;
    }

    case 'token': {
      const token = payload.text || '';
      appendText(token);
      card.textEl.textContent += token;
      break;
    }

    case 'sources': {
      // Remove typing cursor
      card.textEl.classList.remove('cursor');

      const sources = payload.sources || [];
      if (sources.length === 0) break;

      card.citSection.classList.remove('hidden');
      sources.forEach((src, i) => {
        const citCard = renderCitationCard(src, i);
        card.citCards.appendChild(citCard);
      });

      // Scroll to show citations
      setTimeout(() => card.el.scrollIntoView({ behavior: 'smooth', block: 'end' }), 100);
      break;
    }

    case 'done': {
      card.textEl.classList.remove('cursor');
      STAGES.forEach(completeStage);
      stageMsg.textContent = 'Complete';
      break;
    }

    case 'error': {
      card.textEl.classList.remove('cursor');
      const stage = payload.stage || '';
      const msg   = payload.message || 'Unknown error';
      showError(`[${stage || 'pipeline'}] ${msg}`);

      // Show error inline in card
      card.textEl.textContent = '⚠ ' + msg;
      card.textEl.classList.add('text-red-400');

      if (stage && STAGES.includes(stage)) activateStage(stage, msg);
      break;
    }

    default:
      break;
  }
}

// ─── Input bindings ───────────────────────────────────────────────────────────
searchBtn.addEventListener('click', runSearch);

searchInput.addEventListener('keydown', e => {
  // Cmd+Enter / Ctrl+Enter to submit
  if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
    e.preventDefault();
    runSearch();
  }
});

// ─── Utility ──────────────────────────────────────────────────────────────────
function escHtml(str) {
  return String(str)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}

// ─── Init ─────────────────────────────────────────────────────────────────────
searchInput.focus();
