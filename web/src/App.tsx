import { RouterProvider } from "react-router-dom";
import { router } from "./pages/Router";

function App() {
    // No padding: every Rapido page is a full-bleed shell that paints its own
    // background edge to edge.
    return (
        <main>
            <RouterProvider router={router} />
        </main>
    );
}

export default App;
