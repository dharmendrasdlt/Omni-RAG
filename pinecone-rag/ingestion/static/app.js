const dropZone = document.getElementById("dropZone");
const fileInput = document.getElementById("fileInput");
const fileCard = document.getElementById("fileCard");
const fileName = document.getElementById("fileName");
const fileSize = document.getElementById("fileSize");
const errorBanner = document.getElementById("errorBanner");
const errorModal = document.getElementById("errorModal");
const modalErrorText = document.getElementById("modalErrorText");
const modalResetBtn = document.getElementById("modalResetBtn");
const resetBtn = document.getElementById("resetBtn");
const globalProgress = document.getElementById("globalProgress");
const progressLabel = document.getElementById("progressLabel");
const statusPill = document.getElementById("statusPill");
const statusCard = document.getElementById("statusCard");
const connectionState = document.getElementById("connectionState");
const jobIdEl = document.getElementById("jobId");
const fileIdEl = document.getElementById("fileId");
const chunkProgress = document.getElementById("chunkProgress");

let eventSource = null;

dropZone.addEventListener("dragover", (event) => {
  event.preventDefault();
  dropZone.classList.add("border-emerald-500", "bg-emerald-500/10");
});

dropZone.addEventListener("dragleave", () => {
  dropZone.classList.remove("border-emerald-500", "bg-emerald-500/10");
});

dropZone.addEventListener("drop", (event) => {
  event.preventDefault();
  dropZone.classList.remove("border-emerald-500", "bg-emerald-500/10");
  const [file] = event.dataTransfer.files;
  handleFile(file);
});

fileInput.addEventListener("change", () => {
  const [file] = fileInput.files;
  handleFile(file);
});

resetBtn.addEventListener("click", resetUI);
modalResetBtn.addEventListener("click", resetUI);

async function handleFile(file) {
  hideError();
  if (!file) return;

  if (file.type !== "application/pdf" && !file.name.toLowerCase().endsWith(".pdf")) {
    showBanner("Only PDF files are supported. Please choose a .pdf document.");
    return;
  }

  dropZone.classList.add("hidden");
  fileCard.classList.remove("hidden");
  resetBtn.classList.remove("hidden");
  fileName.textContent = file.name;
  fileSize.textContent = `${formatBytes(file.size)} PDF`;
  statusPill.textContent = "Uploading";
  statusPill.className = "rounded-full border border-sky-500/40 bg-sky-500/10 px-3 py-1 text-xs font-medium text-sky-300";
  setProgress(4);
  setStage(1, "processing", "Uploading PDF to backend...");

  const form = new FormData();
  form.append("file", file);

  try {
    const response = await fetch("/api/upload", {
      method: "POST",
      body: form,
    });

    const payload = await response.json();
    if (!response.ok) {
      throw new Error(payload.error || "Upload failed.");
    }

    jobIdEl.textContent = payload.job_id;
    fileIdEl.textContent = payload.file_id;
    connectEvents(payload.job_id);
  } catch (error) {
    handlePipelineError({ error: error.message, message: error.message, stage: 1 });
  }
}

function connectEvents(jobId) {
  if (eventSource) eventSource.close();
  connectionState.textContent = "Connected";
  connectionState.className = "rounded-full border border-emerald-500/40 bg-emerald-500/10 px-3 py-1 text-xs text-emerald-300";

  eventSource = new EventSource(`/api/events/${encodeURIComponent(jobId)}`);
  eventSource.addEventListener("status", (event) => {
    const payload = JSON.parse(event.data);
    applyEvent(payload);
  });
  eventSource.onerror = () => {
    connectionState.textContent = "Reconnecting";
    connectionState.className = "rounded-full border border-amber-500/40 bg-amber-500/10 px-3 py-1 text-xs text-amber-300";
  };
}

function applyEvent(event) {
  if (event.file_id) fileIdEl.textContent = event.file_id;
  if (typeof event.progress === "number") setProgress(event.progress);

  if (event.status === "error") {
    handlePipelineError(event);
    return;
  }

  if (event.stage > 0) {
    let text = event.message || "Processing...";
    if (event.stage === 2 && event.current_page && event.total_pages) {
      text = `Reading Page ${event.current_page} of ${event.total_pages}...`;
    }
    if (event.stage === 3 && event.current_chunk && event.total_chunks) {
      text = `Generated vector ${event.current_chunk} of ${event.total_chunks}`;
      chunkProgress.style.width = `${Math.round((event.current_chunk / event.total_chunks) * 100)}%`;
    }
    if (event.stage === 4 && event.indexed && event.total_vectors) {
      text = `Indexed ${event.indexed} of ${event.total_vectors} vectors`;
    }
    setStage(event.stage, event.status, text);
  }

  if (event.status === "completed" && event.stage === 0) {
    statusPill.textContent = "Complete";
    statusPill.className = "rounded-full border border-emerald-500/40 bg-emerald-500/10 px-3 py-1 text-xs font-medium text-emerald-300";
    connectionState.textContent = "Complete";
    setProgress(100);
    if (eventSource) eventSource.close();
  }
}

function setStage(stageNumber, status, text) {
  const node = document.querySelector(`.stage[data-stage="${stageNumber}"]`);
  if (!node) return;
  const indicator = node.querySelector(".indicator");
  const stageText = node.querySelector(".stage-text");
  stageText.textContent = text;

  node.classList.remove("border-slate-800", "border-emerald-500/50", "border-sky-500/50", "border-red-500/60");
  indicator.className = "indicator mt-1 h-4 w-4 rounded-full";

  if (status === "completed") {
    node.classList.add("border-emerald-500/50");
    indicator.classList.add("bg-emerald-400", "border", "border-emerald-300");
    stageText.className = "stage-text mt-1 text-sm text-emerald-300";
  } else if (status === "processing") {
    node.classList.add("border-sky-500/50");
    indicator.classList.add("animate-pulse", "bg-sky-400", "border", "border-sky-300");
    stageText.className = "stage-text mt-1 text-sm text-sky-300";
  } else {
    node.classList.add("border-slate-800");
    indicator.classList.add("border", "border-slate-600", "bg-slate-800");
    stageText.className = "stage-text mt-1 text-sm text-slate-500";
  }
}

function handlePipelineError(event) {
  const message = event.error || event.message || "Unknown pipeline failure.";
  statusCard.classList.remove("border-slate-800");
  statusCard.classList.add("border-red-500/70");
  statusPill.textContent = "Failed";
  statusPill.className = "rounded-full border border-red-500/40 bg-red-500/10 px-3 py-1 text-xs font-medium text-red-300";
  connectionState.textContent = "Halted";
  connectionState.className = "rounded-full border border-red-500/40 bg-red-500/10 px-3 py-1 text-xs text-red-300";
  if (event.stage) setStage(event.stage, "error", message);
  modalErrorText.textContent = message;
  errorModal.classList.remove("hidden");
  errorModal.classList.add("flex");
  if (eventSource) eventSource.close();
}

function setProgress(value) {
  const clamped = Math.max(0, Math.min(100, value));
  globalProgress.style.width = `${clamped}%`;
  progressLabel.textContent = `${clamped}%`;
}

function showBanner(message) {
  errorBanner.textContent = message;
  errorBanner.classList.remove("hidden");
}

function hideError() {
  errorBanner.classList.add("hidden");
  errorModal.classList.add("hidden");
  errorModal.classList.remove("flex");
}

function resetUI() {
  if (eventSource) eventSource.close();
  eventSource = null;
  fileInput.value = "";
  hideError();
  dropZone.classList.remove("hidden");
  fileCard.classList.add("hidden");
  resetBtn.classList.add("hidden");
  statusCard.classList.remove("border-red-500/70");
  statusCard.classList.add("border-slate-800");
  statusPill.textContent = "Queued";
  connectionState.textContent = "Waiting";
  connectionState.className = "rounded-full border border-slate-700 px-3 py-1 text-xs text-slate-400";
  jobIdEl.textContent = "-";
  fileIdEl.textContent = "-";
  chunkProgress.style.width = "0%";
  setProgress(0);
  [1, 2, 3, 4].forEach((stage) => setStage(stage, "idle", defaultStageText(stage)));
}

function defaultStageText(stage) {
  return {
    1: "Awaiting PDF upload.",
    2: "Page counters will appear here.",
    3: "Chunk worker pool is idle.",
    4: "Waiting for vectors.",
  }[stage];
}

function formatBytes(bytes) {
  if (bytes === 0) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  const index = Math.floor(Math.log(bytes) / Math.log(1024));
  return `${(bytes / Math.pow(1024, index)).toFixed(1)} ${units[index]}`;
}
