import UIKit

// Request time for work already in progress when the user switches apps.
// UIKit decides how much time is available. Expiration ends the task and
// leaves it to the next foreground activation to request another one.
final class ColaunchMobileBackgroundPlugin: MyGoPlugin {
    private var task: UIBackgroundTaskIdentifier = .invalid

    func call(_ method: String, _ args: [String: Any], _ reply: @escaping PluginReply) {
        guard method == "set" else {
            reply(.failure(PluginFailure("NoMethod", method)))
            return
        }
        guard let active = args["active"] as? Bool else {
            reply(.failure(PluginFailure("InvalidArgument", "active must be a boolean")))
            return
        }
        if active {
            if task == .invalid && UIApplication.shared.applicationState != .background {
                task = UIApplication.shared.beginBackgroundTask(withName: "Colaunch remote work") { [weak self] in
                    self?.endTask()
                }
            }
        } else {
            endTask()
        }
        reply(.success(nil))
    }

    private func endTask() {
        guard task != .invalid else { return }
        let identifier = task
        task = .invalid
        UIApplication.shared.endBackgroundTask(identifier)
    }
}
