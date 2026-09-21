// @vitest-environment jsdom
import { act, cleanup, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { api } from '../api'
import { useConnections } from './useConnections'
import type { ConnectionObservation } from '../types'

vi.mock('../api', () => ({ api: { connections: vi.fn() } }))
beforeEach(() => { vi.useFakeTimers(); Object.defineProperty(document, 'hidden', { configurable: true, value: false }) })
afterEach(() => { cleanup(); vi.useRealTimers(); vi.clearAllMocks() })

it('does not apply an in-flight response after pausing, and resumes with a fresh sample', async () => {
  let finish!: (value: ConnectionObservation) => void
  vi.mocked(api.connections).mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
  const result = renderHook(({ paused }) => useConnections(paused), { initialProps: { paused: false } })
  expect(api.connections).toHaveBeenCalledTimes(1)
  result.rerender({ paused: true })
  await act(async () => finish({ sampled_at: 'old' } as ConnectionObservation))
  expect(result.result.current.snapshot).toBeNull()
  await act(async () => { await vi.advanceTimersByTimeAsync(6000) })
  expect(api.connections).toHaveBeenCalledTimes(1)
  vi.mocked(api.connections).mockResolvedValue({ sampled_at: 'fresh' } as ConnectionObservation)
  await act(async () => result.rerender({ paused: false }))
  expect(result.result.current.snapshot?.sampled_at).toBe('fresh')
})

it('retains the last sample on error, recovers, and stops when unmounted', async () => {
  vi.mocked(api.connections).mockResolvedValueOnce({ sampled_at: 'first' } as ConnectionObservation).mockRejectedValueOnce(new Error('disconnected')).mockResolvedValue({ sampled_at: 'recovered' } as ConnectionObservation)
  const result = renderHook(() => useConnections(false))
  await act(async () => {})
  await act(async () => { await vi.advanceTimersByTimeAsync(2000) })
  expect(result.result.current.snapshot?.sampled_at).toBe('first')
  expect(result.result.current.error).toBe('disconnected')
  await act(async () => { await vi.advanceTimersByTimeAsync(2000) })
  expect(result.result.current.snapshot?.sampled_at).toBe('recovered')
  expect(result.result.current.error).toBe('')
  result.unmount()
  await act(async () => { await vi.advanceTimersByTimeAsync(10000) })
  expect(api.connections).toHaveBeenCalledTimes(3)
})
