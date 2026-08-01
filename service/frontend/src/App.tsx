import { BrowserRouter, Route, Routes } from 'react-router'
import { Chrome } from './components/Chrome'
import { ContractPage } from './pages/Contract'
import { CreatePage } from './pages/Create'
import { LobbyPage } from './pages/Lobby'
import { OraclePage } from './pages/Oracle'

export default function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route element={<Chrome />}>
          <Route index element={<LobbyPage />} />
          <Route path="contracts/new" element={<CreatePage />} />
          <Route path="contracts/:id" element={<ContractPage />} />
          <Route path="oracle" element={<OraclePage />} />
        </Route>
      </Routes>
    </BrowserRouter>
  )
}
