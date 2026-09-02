// evilcode web — transcript rendering. The renderer only touches the DOM when a
// public function is called; importing this module is safe in non-browser tests.

function asText(value) {
  if (value == null) return "";
  try { return String(value); } catch { return ""; }
}

function documentFor(parent, options) {
  const supplied = options?.document ?? options?.doc;
  if (supplied && typeof supplied.createElement === "function") return supplied;
  if (parent?.ownerDocument && typeof parent.ownerDocument.createElement === "function") {
    return parent.ownerDocument;
  }
  if (typeof document !== "undefined" && typeof document.createElement === "function") return document;
  throw new Error("a DOM document is required to render a transcript");
}

function element(doc, tag, className, text) {
  const node = doc.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = asText(text);
  return node;
}

function textNode(doc, text) {
  const node = doc.createTextNode("");
  node.textContent = asText(text);
  return node;
}

function clear(node) {
  if (typeof node.replaceChildren === "function") {
    node.replaceChildren();
    return;
  }
  while (node.firstChild) node.removeChild(node.firstChild);
}

function appendText(parent, doc, text) {
  if (text) parent.appendChild(textNode(doc, text));
}

function firstDefined(object, keys, fallback = undefined) {
  for (const key of keys) {
    if (object && object[key] !== undefined && object[key] !== null) return object[key];
  }
  return fallback;
}

function integer(value, fallback = 0) {
  const number = Number(value);
  return Number.isFinite(number) ? Math.max(0, Math.trunc(number)) : fallback;
}

function normalizeKind(message) {
  const raw = asText(message?.kind ?? message?.type ?? message?.role).toLowerCase();
  if (raw === "text" || raw === "model" || raw === "ai") return "assistant";
  if (raw === "thinking" || raw === "thought") return "reasoning";
  if (raw === "tool_call" || raw === "function") return "tool";
  if (raw === "token_usage") return "usage";
  if (raw === "memory_recall") return "memory";
  if (["warn", "warning", "info", "success"].includes(raw)) return "notice";
  if (raw === "system_message") return "system";
  return raw || "assistant";
}

function roleClass(kind, message) {
  switch (kind) {
    case "user": return "msg--user";
    case "tool": return "msg--tool";
    case "reasoning":
    case "assistant": return "msg--ai";
    case "notice":
    case "error":
    case "memory":
    case "usage":
    case "system": return "msg--system";
    default:
      return message?.role === "user" ? "msg--user" : message?.role === "tool" ? "msg--tool" : "msg--ai";
  }
}

function appendMeta(card, doc, label) {
  card.appendChild(element(doc, "div", "msg-meta", label));
}

function levelOf(value, fallback = "info") {
  const level = asText(value).toLowerCase();
  if (level === "warn") return "warning";
  if (level === "danger") return "error";
  if (["info", "success", "warning", "error"].includes(level)) return level;
  return fallback;
}

function reasoningBlock(doc, message, options = {}) {
  const details = element(doc, "details", "msg-reasoning reasoning");
  // Reasoning is deliberately closed once complete, but remains visible while
  // the model is actively streaming it.
  details.open = options.reasoningOpen === true || options.expandReasoning === true ||
    message?.expanded === true || message?.streaming === true;
  const content = message?.content ?? message?.text ?? message?.reasoning ?? "";
  const lines = asText(content).split("\n").length;
  details.appendChild(element(doc, "summary", "reasoning-summary", lines > 1 ? `Reasoning · ${lines} lines` : "Reasoning"));
  const body = element(doc, "div", "msg-body reasoning-body");
  renderMarkdown(body, content);
  if (message?.streaming) {
    const caret = element(doc, "span", "stream-caret", "▌");
    caret.setAttribute("aria-hidden", "true");
    body.appendChild(caret);
  }
  details.appendChild(body);
  return details;
}

function isHistory(message, options) {
  return !!(
    options?.history || options?.isHistory || options?.live === false || options?.mode === "history" ||
    message?.history || message?.isHistory || message?.is_history || message?.fromHistory
  );
}

function imageValues(message) {
  const value = message?.images ?? message?.image;
  if (value == null) return [];
  return Array.isArray(value) ? value : [value];
}

function omittedImageCount(message, images) {
  if (images.length) return images.length;
  for (const key of ["image_count", "imageCount", "images_count", "imagesOmitted", "images_omitted"]) {
    const count = integer(message?.[key]);
    if (count > 0) return count;
  }
  return message?.has_images || message?.hasImages || message?.images_omitted || message?.imagesOmitted ? 1 : 0;
}

function imageWasOmitted(message) {
  return !!(message?.imagesOmitted || message?.images_omitted || message?.imageOmitted || message?.image_omitted);
}

function safeImageMime(value) {
  const mime = asText(value).toLowerCase().split(";", 1)[0].trim();
  if (mime === "image/jpg") return "image/jpeg";
  return ["image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp"].includes(mime) ? mime : "";
}

function decodeBase64(value) {
  let input = asText(value).replace(/\s+/g, "").replace(/-/g, "+").replace(/_/g, "/");
  if (!input || !/^[A-Za-z0-9+/]*={0,2}$/.test(input) || input.length % 4 === 1) return null;
  input += "=".repeat((4 - (input.length % 4)) % 4);
  try {
    if (typeof atob === "function") {
      const binary = atob(input);
      const bytes = new Uint8Array(binary.length);
      for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
      return bytes;
    }
  } catch {
    return null;
  }
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
  const bytes = [];
  for (let i = 0; i < input.length; i += 4) {
    const a = alphabet.indexOf(input[i]);
    const b = alphabet.indexOf(input[i + 1]);
    const c = input[i + 2] === "=" ? 0 : alphabet.indexOf(input[i + 2]);
    const d = input[i + 3] === "=" ? 0 : alphabet.indexOf(input[i + 3]);
    if (a < 0 || b < 0 || c < 0 || d < 0) return null;
    bytes.push((a << 2) | (b >> 4));
    if (input[i + 2] !== "=") bytes.push(((b & 15) << 4) | (c >> 2));
    if (input[i + 3] !== "=") bytes.push(((c & 3) << 6) | d);
  }
  return new Uint8Array(bytes);
}

function bytesFrom(value) {
  if (value == null) return null;
  if (typeof value === "string") {
    const match = /^data:([^;,]+);base64,(.*)$/is.exec(value.trim());
    return decodeBase64(match ? match[2] : value);
  }
  if (Array.isArray(value)) {
    try { return Uint8Array.from(value.map((item) => Math.max(0, Math.min(255, integer(item))))); } catch { return null; }
  }
  if (typeof ArrayBuffer !== "undefined") {
    if (value instanceof ArrayBuffer) return new Uint8Array(value);
    if (ArrayBuffer.isView?.(value)) return new Uint8Array(value.buffer, value.byteOffset, value.byteLength);
  }
  if (typeof value === "object") {
    for (const key of ["bytes", "data", "base64", "image"]) {
      if (value[key] !== undefined) return bytesFrom(value[key]);
    }
  }
  return null;
}

function imageMime(bytes, hint) {
  const hinted = safeImageMime(hint);
  if (hinted) return hinted;
  if (bytes?.length >= 8 && bytes[0] === 0x89 && bytes[1] === 0x50 && bytes[2] === 0x4e && bytes[3] === 0x47) return "image/png";
  if (bytes?.length >= 3 && bytes[0] === 0xff && bytes[1] === 0xd8 && bytes[2] === 0xff) return "image/jpeg";
  if (bytes?.length >= 6 && bytes[0] === 0x47 && bytes[1] === 0x49 && bytes[2] === 0x46) return "image/gif";
  if (bytes?.length >= 12 && bytes[0] === 0x52 && bytes[1] === 0x49 && bytes[2] === 0x46 && bytes[3] === 0x46 &&
      bytes[8] === 0x57 && bytes[9] === 0x45 && bytes[10] === 0x42 && bytes[11] === 0x50) return "image/webp";
  if (bytes?.length >= 2 && bytes[0] === 0x42 && bytes[1] === 0x4d) return "image/bmp";
  return "image/png";
}

function encodeBase64(bytes) {
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
  let output = "";
  for (let i = 0; i < bytes.length; i += 3) {
    const a = bytes[i];
    const b = i + 1 < bytes.length ? bytes[i + 1] : 0;
    const c = i + 2 < bytes.length ? bytes[i + 2] : 0;
    output += alphabet[a >> 2];
    output += alphabet[((a & 3) << 4) | (b >> 4)];
    output += i + 1 < bytes.length ? alphabet[((b & 15) << 2) | (c >> 6)] : "=";
    output += i + 2 < bytes.length ? alphabet[c & 63] : "=";
  }
  return output;
}

function safeImageURL(value) {
  const url = asText(value).trim();
  if (/^blob:[^\s<>"']+$/i.test(url)) return url;
  if (/^data:image\/(?:png|jpeg|gif|webp|bmp);base64,[A-Za-z0-9+/=_-]+$/i.test(url)) return url;
  return "";
}

function imageURL(value, doc, options = {}) {
  if (typeof options.imageURL === "function") {
    try {
      const resolved = safeImageURL(options.imageURL(value));
      if (resolved) return resolved;
    } catch { /* fall through to the byte decoder */ }
  }
  if (typeof value === "object" && value !== null) {
    const direct = safeImageURL(value.url ?? value.src);
    if (direct) return direct;
  }
  if (typeof value === "string") {
    const direct = safeImageURL(value.trim());
    if (direct) return direct;
  }
  const bytes = bytesFrom(typeof value === "object" && value !== null && value.base64 !== undefined ? value.base64 : value);
  if (!bytes || !bytes.length) return "";
  const hint = typeof value === "object" && value !== null ? value.mime ?? value.type : options.imageMime;
  const mime = imageMime(bytes, hint);
  const view = doc.defaultView || (typeof window !== "undefined" ? window : globalThis);
  const BlobCtor = view?.Blob || (typeof Blob !== "undefined" ? Blob : null);
  const URLCtor = view?.URL || (typeof URL !== "undefined" ? URL : null);
  if (BlobCtor && URLCtor && typeof URLCtor.createObjectURL === "function") {
    try { return URLCtor.createObjectURL(new BlobCtor([bytes], { type: mime })); } catch { /* data URL fallback */ }
  }
  return `data:${mime};base64,${encodeBase64(bytes)}`;
}

function appendImages(parent, doc, message, options = {}, className = "msg-images") {
  const images = imageValues(message);
  const count = omittedImageCount(message, images);
  if (!count) return;
  const box = element(doc, "div", className);
  if (isHistory(message, options) || imageWasOmitted(message) || images.length < count) {
    for (let i = 0; i < count; i++) box.appendChild(element(doc, "div", "msg-img-placeholder", "image omitted from history"));
  } else {
    for (const value of images) {
      const src = imageURL(value, doc, options);
      if (!src) {
        box.appendChild(element(doc, "div", "msg-img-placeholder", "image unavailable"));
        continue;
      }
      const image = element(doc, "img", "msg-image");
      image.src = src;
      image.alt = "attached image";
      box.appendChild(image);
    }
  }
  parent.appendChild(box);
}

function closeMarker(source, marker, start) {
  const end = source.indexOf(marker, start);
  return end > start ? end : -1;
}

function safeHref(href) {
  const value = asText(href).trim();
  if (/^https?:\/\//i.test(value) || /^mailto:/i.test(value) || value.startsWith("/") || value.startsWith("#")) return value;
  return "";
}

function renderInline(parent, source, doc, depth = 0) {
  if (depth > 8) {
    appendText(parent, doc, source);
    return;
  }
  let plain = "";
  const flush = () => {
    if (plain) appendText(parent, doc, plain);
    plain = "";
  };
  for (let i = 0; i < source.length;) {
    if (source[i] === "\\" && i + 1 < source.length && "*_`~\\[]".includes(source[i + 1])) {
      plain += source[i + 1];
      i += 2;
      continue;
    }
    if (source[i] === "`") {
      let run = 1;
      while (source[i + run] === "`") run++;
      const marker = "`".repeat(run);
      const end = closeMarker(source, marker, i + run);
      if (end >= 0) {
        flush();
        parent.appendChild(element(doc, "code", "md-code-span", source.slice(i + run, end).replace(/\n/g, " ")));
        i = end + run;
        continue;
      }
    }
    let marker = "";
    if (source.startsWith("**", i) || source.startsWith("__", i)) marker = source.slice(i, i + 2);
    else if (source.startsWith("~~", i)) marker = "~~";
    if (marker) {
      const end = closeMarker(source, marker, i + marker.length);
      if (end >= 0 && source.slice(i + marker.length, end).trim()) {
        flush();
        const strong = element(doc, marker === "~~" ? "del" : "strong", marker === "~~" ? "md-strike" : "md-strong");
        renderInline(strong, source.slice(i + marker.length, end), doc, depth + 1);
        parent.appendChild(strong);
        i = end + marker.length;
        continue;
      }
    }
    if (source[i] === "*" || source[i] === "_") {
      const end = closeMarker(source, source[i], i + 1);
      if (end >= 0 && source.slice(i + 1, end).trim() && !/\s/.test(source[i + 1])) {
        flush();
        const emphasis = element(doc, "em", "md-em");
        renderInline(emphasis, source.slice(i + 1, end), doc, depth + 1);
        parent.appendChild(emphasis);
        i = end + 1;
        continue;
      }
    }
    if (source[i] === "[") {
      const labelEnd = source.indexOf("](", i + 1);
      const urlEnd = labelEnd < 0 ? -1 : source.indexOf(")", labelEnd + 2);
      if (labelEnd > i + 1 && urlEnd > labelEnd + 2) {
        const href = safeHref(source.slice(labelEnd + 2, urlEnd));
        if (href) {
          flush();
          const link = element(doc, "a", "md-link");
          link.setAttribute("href", href);
          link.setAttribute("rel", "noreferrer noopener");
          renderInline(link, source.slice(i + 1, labelEnd), doc, depth + 1);
          parent.appendChild(link);
          i = urlEnd + 1;
          continue;
        }
      }
    }
    if (source[i] === "<") {
      const end = source.indexOf(">", i + 1);
      if (end > i + 1) {
        const href = safeHref(source.slice(i + 1, end));
        if (href && /^https?:\/\//i.test(href)) {
          flush();
          const link = element(doc, "a", "md-link", href);
          link.setAttribute("href", href);
          link.setAttribute("rel", "noreferrer noopener");
          parent.appendChild(link);
          i = end + 1;
          continue;
        }
      }
    }
    plain += source[i++];
  }
  flush();
}

function isBlockStart(line) {
  return /^\s*(?:```+|~~~+|#{1,6}\s|[-*+]\s+|\d+[.)]\s+|>\s?)/.test(line) || /^\s{0,3}([-*_])(?:\s*\1){2,}\s*$/.test(line);
}

function renderMarkdownBlocks(parent, source, doc) {
  const lines = source.replace(/\r\n?/g, "\n").split("\n");
  let index = 0;
  while (index < lines.length) {
    if (!lines[index].trim()) {
      index++;
      continue;
    }
    const fence = /^\s*(`{3,}|~{3,})\s*([A-Za-z0-9_+-]*)\s*$/.exec(lines[index]);
    if (fence) {
      const marker = fence[1][0];
      const body = [];
      index++;
      while (index < lines.length && !new RegExp(`^\\s*${marker}{${fence[1].length},}\\s*$`).test(lines[index])) body.push(lines[index++]);
      if (index < lines.length) index++;
      const pre = element(doc, "pre", "md-code-block");
      if (fence[2]) pre.setAttribute("data-language", fence[2]);
      pre.appendChild(element(doc, "code", undefined, body.join("\n")));
      parent.appendChild(pre);
      continue;
    }
    const heading = /^(#{1,6})[ \t]+(.+?)\s*#*\s*$/.exec(lines[index]);
    if (heading) {
      const headingNode = element(doc, `h${heading[1].length}`, "md-heading");
      renderInline(headingNode, heading[2], doc);
      parent.appendChild(headingNode);
      index++;
      continue;
    }
    if (/^\s*>/.test(lines[index])) {
      const quote = [];
      while (index < lines.length && /^\s*>/.test(lines[index])) quote.push(lines[index++].replace(/^\s*> ?/, ""));
      const blockquote = element(doc, "blockquote", "md-blockquote");
      renderMarkdownBlocks(blockquote, quote.join("\n"), doc);
      parent.appendChild(blockquote);
      continue;
    }
    const unordered = /^\s*[-*+]\s+(.+)$/.exec(lines[index]);
    const ordered = /^\s*\d+[.)]\s+(.+)$/.exec(lines[index]);
    if (unordered || ordered) {
      const list = element(doc, ordered ? "ol" : "ul", "md-list");
      while (index < lines.length) {
        const item = (ordered ? /^\s*\d+[.)]\s+(.+)$/ : /^\s*[-*+]\s+(.+)$/).exec(lines[index]);
        if (!item) break;
        const li = element(doc, "li");
        renderInline(li, item[1], doc);
        list.appendChild(li);
        index++;
      }
      parent.appendChild(list);
      continue;
    }
    if (/^\s{0,3}([-*_])(?:\s*\1){2,}\s*$/.test(lines[index])) {
      parent.appendChild(element(doc, "hr", "md-rule"));
      index++;
      continue;
    }
    const paragraph = [lines[index++]];
    while (index < lines.length && lines[index].trim() && !isBlockStart(lines[index])) paragraph.push(lines[index++]);
    const p = element(doc, "p", "md-paragraph");
    renderInline(p, paragraph.join("\n"), doc);
    parent.appendChild(p);
  }
}

export function renderMarkdown(parent, text) {
  const doc = documentFor(parent);
  clear(parent);
  const source = asText(text);
  if (source) renderMarkdownBlocks(parent, source, doc);
  return parent;
}

export function renderDiff(parent, diff) {
  const doc = documentFor(parent);
  clear(parent);
  const source = asText(diff);
  if (!source) return parent;
  const pre = element(doc, "pre", "diff");
  for (const line of source.replace(/\r\n?/g, "\n").split("\n")) {
    let suffix = "";
    if (line.startsWith("+") && !line.startsWith("+++")) suffix = " diff-add";
    else if (line.startsWith("-") && !line.startsWith("---")) suffix = " diff-del";
    else if (line.startsWith("@@")) suffix = " diff-hunk";
    pre.appendChild(element(doc, "div", `diff-line${suffix}`, line));
  }
  parent.appendChild(pre);
  return parent;
}

function usageElement(doc, usage = {}) {
  const input = integer(firstDefined(usage, ["in", "input", "input_tokens", "prompt_tokens"]));
  const output = integer(firstDefined(usage, ["out", "output", "output_tokens", "completion_tokens"]));
  const contextUsed = integer(firstDefined(usage, ["ctx_used", "ctxUsed", "context_used"]));
  const contextMax = integer(firstDefined(usage, ["ctx_max", "ctxMax", "context_max"]));
  const cacheRead = integer(firstDefined(usage, ["cache_read", "cacheRead"]));
  const cacheWrite = integer(firstDefined(usage, ["cache_write", "cacheWrite"]));
  const generationMS = integer(firstDefined(usage, ["gen_ms", "genMs", "generation_ms"]));
  const percent = contextMax ? Math.min(100, Math.max(0, (contextUsed / contextMax) * 100)) : 0;

  const root = element(doc, "div", "usage-meter");
  const meter = element(doc, "div", "meter");
  meter.setAttribute("role", "progressbar");
  meter.setAttribute("aria-valuemin", "0");
  meter.setAttribute("aria-valuemax", String(contextMax));
  meter.setAttribute("aria-valuenow", String(Math.min(contextUsed, contextMax || contextUsed)));
  const fill = element(doc, "div", `meter-fill${percent >= 80 ? " meter-fill--high" : ""}`);
  if (fill.style) fill.style.width = `${percent}%`;
  meter.appendChild(fill);
  root.appendChild(meter);

  const contextText = contextMax ? `${contextUsed} / ${contextMax} context · ${Math.round(percent)}%` : `${contextUsed} context`;
  root.appendChild(element(doc, "div", "meter-readout", contextText));
  const stats = element(doc, "div", "usage-stats");
  stats.appendChild(element(doc, "span", "usage-stat usage-input", `in ${input}`));
  stats.appendChild(element(doc, "span", "usage-stat usage-output", `out ${output}`));
  if (cacheRead || cacheWrite || usage.cache_hit) stats.appendChild(element(doc, "span", "usage-stat usage-cache", `cache ${cacheRead}/${cacheWrite}`));
  if (generationMS) stats.appendChild(element(doc, "span", "usage-stat usage-generation", `${generationMS} ms`));
  root.appendChild(stats);
  return root;
}

export function renderUsage(usage, options = {}) {
  return usageElement(documentFor(options?.parent, options), usage);
}

function parsedArgs(value) {
  if (value && typeof value === "object") return value;
  if (typeof value === "string") {
    try { return JSON.parse(value); } catch { return null; }
  }
  return null;
}

function argumentValue(message) {
  return message?.args !== undefined ? message.args : message?.call?.args;
}

function targetFor(message, args) {
  const explicit = firstDefined(message, ["target", "path_target"]);
  if (explicit !== undefined && asText(explicit)) return asText(explicit);
  const object = parsedArgs(args);
  if (!object || typeof object !== "object" || Array.isArray(object)) return "";
  for (const key of ["path", "file", "target", "url", "query", "cmd", "command"]) {
    if (object[key] !== undefined && asText(object[key])) return asText(object[key]);
  }
  return "";
}

function formatArguments(value) {
  if (value == null || value === "") return "";
  const parsed = parsedArgs(value);
  try {
    const formatted = JSON.stringify(parsed ?? value, null, 2);
    return formatted === undefined ? asText(value) : formatted;
  } catch {
    return asText(value);
  }
}

function toolField(doc, parent, label, value, className) {
  const field = element(doc, "section", `tool-field${className ? ` ${className}` : ""}`);
  field.appendChild(element(doc, "div", "tool-label", label));
  field.appendChild(element(doc, "div", "tool-value", value));
  parent.appendChild(field);
  return field;
}

function toolCard(doc, message, options) {
  const name = asText(message.name ?? message.tool_name ?? message.call?.name) || "tool";
  const args = argumentValue(message);
  const target = targetFor(message, args);
  const intent = asText(message.intent);
  const repairs = Array.isArray(message.repairs) ? message.repairs : [];
  const output = firstDefined(message, ["output", "content"], "");
  const diff = asText(message.diff);
  const failed = !!(message.failed || message.is_error || message.error || message.err);
  const error = asText(message.error ?? message.err) || (failed ? "tool failed" : "");
  const held = !!message.held;
  const status = ["pending", "running", "done"].includes(asText(message.status)) ? asText(message.status) : held ? "held" : failed ? "error" : "done";

  const card = element(doc, "article", `msg msg--tool tool-card tool-card--${status}`);
  appendMeta(card, doc, `tool · ${name}`);
  const body = element(doc, "div", "tool-body");
  const head = element(doc, "div", "tool-head");
  head.appendChild(element(doc, "strong", "tool-name", name));
  head.appendChild(element(doc, "span", `tool-status tool-status--${status}`, status));
  body.appendChild(head);
  if (target) toolField(doc, body, "Target", target, "tool-target");
  if (intent) toolField(doc, body, "Intent", intent, "tool-intent");
  if (repairs.length) {
    const field = element(doc, "section", "tool-field tool-repairs");
    field.appendChild(element(doc, "div", "tool-label", "Repairs"));
    const list = element(doc, "ul", "tool-repair-list");
    repairs.forEach((repair) => list.appendChild(element(doc, "li", undefined, repair)));
    field.appendChild(list);
    body.appendChild(field);
  }
  const formattedArgs = formatArguments(args);
  if (formattedArgs) {
    const field = element(doc, "section", "tool-field tool-arguments");
    field.appendChild(element(doc, "div", "tool-label", "Arguments (JSON)"));
    field.appendChild(element(doc, "pre", "tool-args", formattedArgs));
    body.appendChild(field);
  }
  if (output !== "" && output !== undefined && output !== null) {
    const field = element(doc, "section", "tool-field tool-output");
    field.appendChild(element(doc, "div", "tool-label", "Output"));
    const outputBody = element(doc, "div", "tool-output-body");
    renderMarkdown(outputBody, typeof output === "string" ? output : formatArguments(output));
    field.appendChild(outputBody);
    body.appendChild(field);
  } else if (message.display !== undefined && message.display !== null) {
    const field = element(doc, "section", "tool-field tool-display");
    field.appendChild(element(doc, "div", "tool-label", "Output"));
    const display = typeof message.display === "string" ? asText(message.display) : formatArguments(message.display);
    field.appendChild(element(doc, "pre", "tool-display-body", display));
    body.appendChild(field);
  }
  if (diff) {
    const field = element(doc, "section", "tool-field tool-diff");
    field.appendChild(element(doc, "div", "tool-label", "Diff"));
    const diffBody = element(doc, "div", "tool-diff-body");
    renderDiff(diffBody, diff);
    field.appendChild(diffBody);
    body.appendChild(field);
  }
  if (held) body.appendChild(element(doc, "div", "tool-held callout callout--warning", "held by safety gate"));
  if (error) body.appendChild(element(doc, "div", "tool-error callout callout--error", error));
  appendImages(body, doc, message, options, "tool-images");
  card.appendChild(body);
  return card;
}

function plainCard(doc, message, kind, options) {
  const level = kind === "notice" ? levelOf(message.level) : kind === "error" ? "error" : "";
  const classes = `msg ${roleClass(kind, message)}${level ? ` callout callout--${level} notice notice--${level}` : ""}`;
  const card = element(doc, "article", classes);
  let label = kind === "user" ? "you" : kind === "assistant" ? "assistant" : kind;
  if (kind === "tool") label = `tool · ${asText(message.tool_name)}`;
  appendMeta(card, doc, label);
  if (kind === "reasoning") {
    card.appendChild(reasoningBlock(doc, message, options));
    return card;
  }
  if (kind === "assistant" && (message.reasoning || message.reasoning_text)) {
    card.appendChild(reasoningBlock(doc, { ...message, content: message.reasoning ?? message.reasoning_text }, options));
  }
  const body = element(doc, "div", "msg-body");
  const content = firstDefined(message, ["content", "text"], "");
  if (kind === "notice" || kind === "error" || kind === "system" || kind === "memory") body.textContent = asText(content);
  else renderMarkdown(body, content);
  if (message.streaming && (kind === "assistant" || kind === "reasoning")) {
    const caret = element(doc, "span", "stream-caret", "▌");
    caret.setAttribute("aria-hidden", "true");
    body.appendChild(caret);
  }
  card.appendChild(body);
  appendImages(body, doc, message, options);
  if (kind === "memory" && message.display !== undefined && message.display !== null) {
    const display = element(doc, "pre", "memory-display", typeof message.display === "string" ? message.display : formatArguments(message.display));
    card.appendChild(display);
  }
  return card;
}

export function renderMessage(message = {}, options = {}) {
  const doc = documentFor(options?.parent, options);
  const value = message && typeof message === "object" ? message : { content: message };
  const kind = normalizeKind(value);
  if (kind === "tool") return toolCard(doc, value, options);
  if (kind === "usage") {
    const card = element(doc, "article", "msg msg--system msg--usage usage-card");
    appendMeta(card, doc, "usage");
    card.appendChild(usageElement(doc, value.usage ?? value));
    return card;
  }
  return plainCard(doc, value, kind, options);
}

export function renderTranscript(container, messages, options = {}) {
  const doc = documentFor(container, options);
  clear(container);
  if (options.before?.nodeType) container.appendChild(options.before);
  let rendered = 0;
  for (const message of Array.isArray(messages) ? messages : []) {
    if (!message || message.hidden) continue;
    container.appendChild(renderMessage(message, { ...options, document: doc, parent: container }));
    rendered++;
  }
  if (!rendered) {
    const empty = element(doc, "div", "transcript-empty");
    empty.appendChild(element(doc, "p", "empty-title", "No messages yet"));
    container.appendChild(empty);
  }
  return container;
}
