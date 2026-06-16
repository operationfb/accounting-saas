import { apiFetch } from '@/lib/api'
import { ListProjectsResponseSchema, type Project } from '@/types/project'

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
