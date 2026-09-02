// evilcode web — fetch wrappers over the §4 HTTP surface (plan-web.md §8).
// Every daemon error arrives in the uniform {"error": "..."} shape, so these
// wrappers surface the daemon's own words to the user and throw the status
// alongside for callers that branch on it (401, 404, 409).

export class ApiError extends Error {
  constructor(status, message) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

async function request(path, options) {
  let resp;
  try {
    resp = await fetch(path, options);
  } catch (err) {
    throw new ApiError(0, "the daemon is unreachable");
  }
  if (!resp.ok) {
    let message = `the daemon answered HTTP ${resp.status}`;
    try {
      const body = await resp.json();
      if (body && typeof body.error === "string") message = body.error;
    } catch {
      // Not the uniform shape (or not JSON); the status line still says truth.
    }
    throw new ApiError(resp.status, message);
  }
  return resp.json();
}

export function getJSON(path) {
  return request(path);
}

export function postJSON(path, body) {
  return request(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body ?? {}),
  });
}