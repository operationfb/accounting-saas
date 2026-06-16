import { apiFetch } from '@/lib/api'
import {
  ListProjectsResponseSchema,
  GetProjectResponseSchema,
  type Project,
  type CreateProjectRequest,
} from '@/types/project'

// GET /api/v1/projects — every project in the caller's organisation, newest
// first. The bearer token is attached by apiFetch, and a 401 (expired/invalid
// token) is handled there (logout + redirect to /login). An empty list may
// arrive as null, so we default to [].
//
// Only the list call exists for now; get/create/update land with the project
// entry view in a later change.
export async function listProjects(): Promise<Project[]> {
  const data = await apiFetch<unknown>('/projects', { method: 'GET' })
  return ListProjectsResponseSchema.parse(data).projects ?? []
}

// GET /api/v1/projects/:id — one project, used to pre-fill the edit form. A 404
// (unknown id / other org) surfaces as an ApiError for the caller to show.
export async function getProject(id: string): Promise<Project> {
  const data = await apiFetch<unknown>(`/projects/${encodeURIComponent(id)}`, { method: 'GET' })
  return GetProjectResponseSchema.parse(data).project
}

// POST /api/v1/projects — create a project. Returns the created project; the
// caller navigates with it. A 400 (binding) or 422 (validation, e.g. bad amount /
// date / budget) is thrown as an ApiError for the form to display.
export async function createProject(payload: CreateProjectRequest): Promise<Project> {
  const data = await apiFetch<unknown>('/projects', { method: 'POST', body: payload })
  return GetProjectResponseSchema.parse(data).project
}

// PUT /api/v1/projects/:id — full update. Same payload as create; returns the
// updated project. A 404 or 422 surfaces as an ApiError.
export async function updateProject(id: string, payload: CreateProjectRequest): Promise<Project> {
  const data = await apiFetch<unknown>(`/projects/${encodeURIComponent(id)}`, {
    method: 'PUT',
    body: payload,
  })
  return GetProjectResponseSchema.parse(data).project
}
