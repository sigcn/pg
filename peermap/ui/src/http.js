const apiServer = ''

async function post(url, opts = {}) {
  opts.method = 'POST'
  return await request(url, opts)
}

async function get(url, opts = {}) {
  opts.method = 'GET'
  return await request(url, opts)
}

async function put(url, opts = {}) {
  opts.method = 'PUT'
  return await request(url, opts)
}

async function del(url, opts = {}) {
  opts.method = 'DELETE'
  return await request(url, opts)
}

async function request(url, opts) {
  let options = { method: 'GET' }
  if (opts.method) {
    options.method = opts.method
  }
  if (opts.headers) {
    options.headers = opts.headers
  }
  if (opts.body) {
    options.body = JSON.stringify(opts.body)
  }
  if (opts.session) {
    if (!options.headers) {
      options.headers = {}
    }
    options.headers['X-Token'] = opts.session.secret
  }
  let resp = await fetch(`${apiServer}${url}`, options)
  let r = {}
  if (resp.status != 200 && resp.status != 304) {
    r.code = resp.status
    r.msg = await resp.text()
    return r
  }
  try {
    r = await resp.json()
    r.headers = resp.headers
    return r
  } catch (_) {
    r.headers = resp.headers
    r.code = resp.status
    return r
  }
}

async function download(url, opts) {
  let r = await fetch(url, {
    method: 'GET',
    headers: {
      'X-Token': opts.session.secret,
    },
  })

  if (!r.ok) {
    return {
      code: r.status,
      msg: r.statusText,
    }
  }
  let blob = await r.blob()
  let blobUrl = window.URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = blobUrl
  a.download = getFileNameFromHeaders(r.headers)
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  window.URL.revokeObjectURL(blobUrl)
  return {code: 0}
}

function getFileNameFromHeaders(headers) {
  const contentDisposition = headers.get('Content-Disposition')
  if (contentDisposition) {
    const match = contentDisposition.match(/filename\*?=(?:UTF-8'')?([^;]*)/i)
    if (match && match[1]) {
      return decodeURIComponent(match[1].replace(/"/g, ''))
    }
  }
  return null
}

const http = {
  get: get,
  post: post,
  put: put,
  delete: del,
  download: download,
  request: request,
  apiServer: apiServer,
}
export default http
